#!/usr/bin/env python3
"""Exercise real migration process exits on an isolated, initially fresh Compose project.

Use existing local gateway, shard, and reshard images. Supply an env file with the demo
secrets, VERSION, and a unique COMPOSE_PROJECT_NAME. Containers/volumes are kept
for inspection; this script never deletes volumes. Optional --override changes
port bindings, and --url selects the corresponding Gateway URL.
"""
import argparse
import concurrent.futures
import datetime
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

from demo_stages import DemoStages

ROOT = Path(__file__).resolve().parents[2]


def show_demo_inspection(stages, compose, evidence_dir):
    """Run command-sheet blocks 7--13, pausing before every evidence section."""
    def show_compose(*parts):
        print('$ compose ' + ' '.join(parts), flush=True)
        result = compose(*parts)
        if result.stdout:
            print(result.stdout.rstrip(), flush=True)
        if result.stderr:
            print(result.stderr.rstrip(), flush=True)

    def show_sql(db, query):
        show_compose('exec', '-T', db, 'psql', '-v', 'ON_ERROR_STOP=1',
                     '-U', 'bank', '-d', 'bank', '-c', query)

    stages.pause('Block 7: show retained containers and intentional worker exits')
    show_compose('ps', '-a')

    stages.pause('Block 8: show durable migration phase and routing epoch')
    show_sql('db-coordinator', 'TABLE bank_migration; TABLE bank_routing;')

    stages.pause('Block 9: show account ranges and counts in all three databases')
    for db in ('db-a', 'db-b', 'db-c'):
        print(db, flush=True)
        show_sql(db, 'SELECT min(id),max(id),count(*) FROM bank_accounts;')

    stages.pause('Block 10: show saved stale-write rejection errors')
    for name in ('stale-b-write.log', 'stale-c-epoch.log'):
        print(f'{name}:', flush=True)
        print((evidence_dir / name).read_text().rstrip(), flush=True)

    stages.pause('Block 11: show checkpoints, exact balances and operation count')
    evidence = json.loads((evidence_dir / 'results.json').read_text())
    fields = ('checkpoints', 'initial_balances', 'expected_balances', 'final_balances',
              'account_counts', 'c_replicas', 'prepared_transactions',
              'unresolved_transactions', 'restart_persistence')
    print(json.dumps({key: evidence[key] for key in fields}, indent=2), flush=True)
    print('Distinct successful operations:', len(evidence['operations']), flush=True)

    stages.pause('Block 12: show unresolved and prepared transactions directly from databases')
    show_sql('db-coordinator',
             "SELECT count(*) AS unresolved FROM bank_transactions WHERE status<>'COMPLETED';")
    for db in ('db-a', 'db-b', 'db-c'):
        print(db, flush=True)
        show_sql(db, 'SELECT gid,prepared FROM pg_prepared_xacts WHERE database=current_database();')

    stages.pause('Block 13: show the evidence directory and saved files')
    print('Evidence directory:', evidence_dir, flush=True)
    for path in sorted(evidence_dir.iterdir()):
        print(path.name, flush=True)


def show_results(evidence, evidence_dir):
    """Display the verified evidence without requiring separate jq/SQL commands."""
    print('\n--- Bonus 1: visible evidence ---', flush=True)
    print('Migration checkpoints:')
    for checkpoint in evidence['checkpoints']:
        print(f"  {checkpoint['point']}: phase={checkpoint['phase']}, "
              f"epoch={checkpoint['epoch']}, position={checkpoint['position']}, "
              f"exit={checkpoint['exit_code']}")
    print('Account balances (initial / expected / actual):')
    for account, actual in sorted(evidence['final_balances'].items()):
        print(f"  {account}: {evidence['initial_balances'][account]} / "
              f"{evidence['expected_balances'][account]} / {actual}")
    print('Account counts:', json.dumps(evidence['account_counts'], sort_keys=True))
    print('Migrated-account replicas:', ', '.join(evidence['c_replicas']))
    print('Distinct successful operations:', len(evidence['operations']))
    print('Original request keys replayed: all outcomes unchanged')
    print('Unresolved transactions:', evidence['unresolved_transactions'])
    print('Prepared transactions:', json.dumps(evidence['prepared_transactions'], sort_keys=True))
    print('Restart persistence:', evidence['restart_persistence'])
    for filename in ('stale-b-write.log', 'stale-c-epoch.log'):
        print(f'{filename}:')
        print((evidence_dir / filename).read_text().strip())
    print('Evidence:', evidence_dir, flush=True)
    print('PASS Bonus 1: live re-sharding, crash recovery, stale-write fencing, '
          'exact balances and idempotent replay', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env-file', required=True, type=Path)
    parser.add_argument('--override', type=Path)
    parser.add_argument('--url', default='http://127.0.0.1:3000')
    parser.add_argument('--evidence', required=True, type=Path)
    parser.add_argument('--no-pause', action='store_true',
                        help='Run without waiting for Enter between stages (automation).')
    args = parser.parse_args()
    stages = DemoStages(args.no_pause)
    args.evidence.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    for line in args.env_file.read_text().splitlines():
        if line.strip() and not line.lstrip().startswith('#'):
            key, value = line.split('=', 1)
            env[key.strip()] = value.strip().strip('\"\'')
    project = env['COMPOSE_PROJECT_NAME']
    assert project != 'sharded', 'use an isolated project name'
    command = ['docker', 'compose', '--env-file', str(args.env_file.resolve()),
               '-f', str(ROOT / 'deploy/sharded/compose.yml'),
               '-f', str(ROOT / 'deploy/sharded/resharding.yml')]
    if args.override:
        command += ['-f', str(args.override.resolve())]
    command += ['--profile', 'reshard']

    def compose(*parts, check=True, timeout=120):
        result = subprocess.run(command + list(parts), env=env, text=True,
                                capture_output=True, timeout=timeout)
        if check and result.returncode:
            raise RuntimeError(f'Compose {parts[0]} failed: {result.stderr}')
        return result

    def sql(db, query, check=True):
        return compose('exec', '-T', db, 'psql', '-v', 'ON_ERROR_STOP=1',
                       '-U', 'bank', '-d', 'bank', '-Atc', query, check=check)

    def request(path, body=None, token=None, key=None):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if key:
            headers['Idempotency-Key'] = key
        req = urllib.request.Request(args.url + path,
                json.dumps(body).encode() if body is not None else None, headers)
        try:
            response = urllib.request.urlopen(req, timeout=12)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, json.load(response), dict(response.headers)

    def wait_ready():
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            try:
                if request('/ready')[0] == 200:
                    return
            except (OSError, ValueError):
                pass
            time.sleep(.25)
        raise AssertionError('Gateway did not become ready')

    assert not compose('ps', '-aq').stdout.strip(), 'demo project already has containers'
    stages.pause('start the initial two-shard deployment')
    result = compose('up', '-d', '--no-build', '--pull', 'never', 'gateway')
    (args.evidence / 'startup.log').write_text(result.stderr)
    wait_ready()
    assert sql('db-coordinator', 'SELECT phase FROM bank_migration').stdout.strip() == 'NEW'
    assert 'shard-c1' not in compose('ps', '--services', '--status', 'running').stdout
    print('PASS baseline: two shards ready before C starts', flush=True)
    tokens = {}
    for account in ('A0500', 'A1250', 'A1750'):
        code, reply, _ = request('/login', {'username': account, 'password': env['FIXTURE_PASSWORD']})
        assert code == 200, reply
        tokens[account] = reply['access_token']

    def balance(account):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            code, reply, headers = request(f'/accounts/{account}/balance', token=tokens[account])
            if code == 200:
                return reply['balance'], headers.get('X-Bank-Replica')
            time.sleep(.1)
        raise AssertionError(reply)

    operations = []
    mutex = threading.Lock()

    def operation(source, action, amount, key, destination=None, timeout=120):
        body = {'amount': amount}
        path = f'/accounts/{source}/{action}'
        if action == 'transfer':
            path = '/transfers'
            body.update(source=source, destination=destination)
        deadline = stages.active_time() + timeout
        while stages.active_time() < deadline:
            stages.wait_for_traffic()
            try:
                code, reply, _ = request(path, body, tokens[source], key)
                if code >= 500:
                    time.sleep(.1)
                    continue
                assert code in (200, 202, 409), (code, reply)
                while reply.get('pending'):
                    stages.wait_for_traffic()
                    assert stages.active_time() < deadline, reply
                    time.sleep(.1)
                    code, reply, _ = request('/transactions/' + reply['transaction_id'], token=tokens[source])
                    if code >= 500:
                        break
                else:
                    out = reply.get('outcome', reply)
                    assert out['status'] == 'success', out
                    return out
            except (OSError, ValueError):
                time.sleep(.1)
        raise AssertionError(f'operation timed out: {key}')

    def record(source, action, amount, key, destination=None):
        out = operation(source, action, amount, key, destination)
        with mutex:
            operations.append({'source': source, 'action': action, 'amount': amount,
                               'key': key, 'destination': destination, 'outcome': out})
        return out

    initial = {account: balance(account)[0] for account in tokens}
    print('Initial balances:', json.dumps(initial), flush=True)
    stages.pause('verify local and cross-shard transfers before migration')
    # Confirm both transfer paths before introducing C.
    record('A1750', 'transfer', 3, 'demo-local-before', 'A1250')
    record('A0500', 'transfer', 5, 'demo-cross-before', 'A1750')
    print('PASS pre-migration transfers: local B->B and cross-shard A->B', flush=True)
    stages.pause('add Shard C at runtime with an empty database and two replicas')
    compose('up', '-d', '--no-build', '--pull', 'never', 'db-c', 'shard-c1', 'shard-c2')
    running = compose('ps', '--services', '--status', 'running').stdout.splitlines()
    assert 'shard-c1' in running and 'shard-c2' in running
    assert sql('db-c', 'SELECT count(*) FROM bank_accounts').stdout.strip() == '0'
    print('PASS runtime: C started with its own empty database and two replicas', flush=True)

    checkpoints = []

    def worker(point):
        name = project + '-worker-' + point
        start = time.monotonic()
        result = compose('run', '--pull', 'never', '--name', name, '--no-deps',
                         '-e', 'MIGRATION_CRASH_AT=' + (point if point != 'resume' else ''),
                         'reshard', check=False, timeout=90)
        (args.evidence / f'worker-{point}.log').write_text(result.stdout + result.stderr)
        expected = 0 if point == 'resume' else 86
        assert result.returncode == expected, (point, result.returncode, result.stderr)
        inspection = subprocess.run(['docker', 'inspect', '--format', '{{.State.ExitCode}}', name],
                                    check=True, text=True, capture_output=True)
        assert int(inspection.stdout.strip()) == expected
        state = sql('db-coordinator', "SELECT json_build_object('phase',phase,'position',position,'epoch',(SELECT epoch FROM bank_routing)) FROM bank_migration").stdout.strip()
        checkpoint = dict(point=point, exit_code=expected, duration_seconds=round(time.monotonic()-start, 3), **json.loads(state))
        checkpoints.append(checkpoint)
        print('PASS process exit:', json.dumps(checkpoint), flush=True)
        return checkpoint

    def traffic(lane):
        # Independent idempotency keys; contention and retries overlap the worker.
        for i in range(16):
            if lane == 0:
                record('A1750', 'deposit', 1, f'live-deposit-{i}')
            elif lane == 1:
                source, dest = ('A0500', 'A1750') if i % 2 == 0 else ('A1750', 'A0500')
                record(source, 'transfer', 1, f'live-transfer-{i}', dest)
            else:
                record('A1250', 'deposit', 1, f'unaffected-deposit-{i}')
            time.sleep(.03)

    stages.pause('start concurrent traffic and crash the worker during snapshot')
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
        futures = [executor.submit(traffic, lane) for lane in range(3)]
        assert worker('snapshot')['phase'] == 'SNAPSHOT'
        assert sql('db-c', 'SELECT count(*) FROM bank_accounts').stdout.strip() == '0', 'uncommitted snapshot survived crash'
        # Force a durable post-snapshot delta so the replay crash point is reached.
        record('A1750', 'deposit', 7, 'after-snapshot-crash')
        assert int(sql('db-b', 'SELECT count(*) FROM bank_changes').stdout) > 0
        print('PASS snapshot rollback: target empty; durable source changes captured', flush=True)
        stages.pause('recover snapshot and crash after replay batch commit')
        assert worker('replay')['phase'] == 'REPLAY'
        stages.pause('recover replay and crash after fencing the old owner')
        assert worker('fencing')['phase'] == 'FENCING'
        guard = sql('db-b', 'SELECT writable,epoch FROM bank_range_guard').stdout.strip()
        assert guard == 'f|2', guard
        stale = sql('db-b', "UPDATE bank_accounts SET balance=balance+1000000 WHERE id='A1750'", check=False)
        assert stale.returncode and 'stale shard epoch or fenced owner' in stale.stderr
        (args.evidence / 'stale-b-write.log').write_text(stale.stderr)
        record('A1250', 'deposit', 1, 'unaffected-during-fence')
        code, reply, _ = request('/accounts/A1750/deposit', {'amount': 1}, tokens['A1750'], 'probe-cutover')
        assert code == 503 and reply['error'] == 'range_cutover', (code, reply)
        print('PASS fencing: stale B write rejected; unaffected deposit succeeded; '
              'migrating deposit returned 503 range_cutover', flush=True)
        stages.pause('activate epoch 2 and crash before cleanup')
        checkpoint = worker('activation')
        assert checkpoint['phase'] == 'ACTIVATING' and checkpoint['epoch'] == 2
        stale = sql('db-c', "UPDATE bank_accounts SET balance=balance+1000000 WHERE id='A1750'", check=False)
        assert stale.returncode and 'stale shard epoch or fenced owner' in stale.stderr
        (args.evidence / 'stale-c-epoch.log').write_text(stale.stderr)
        print('PASS activation: epoch 2 durable; stale C write rejected', flush=True)
        stages.pause('resume the worker and finish cleanup')
        assert worker('resume')['phase'] == 'DONE'
        for future in futures:
            future.result()
    wait_ready()
    stages.pause('verify post-migration transfers, duplicate requests and exact balances')
    # Use C's new audit sequence and both cross-shard directions after activation.
    record('A1750', 'deposit', 2, 'post-move-deposit')
    record('A1750', 'transfer', 4, 'post-move-cross', 'A0500')
    record('A0500', 'transfer', 3, 'post-move-reverse', 'A1750')
    for item in list(operations):
        replay = operation(item['source'], item['action'], item['amount'], item['key'], item['destination'])
        assert replay == item['outcome'], ('replayed result changed', item, replay)
    expected = dict(initial)
    for item in operations:
        if item['action'] == 'deposit':
            expected[item['source']] += item['amount']
        else:
            expected[item['source']] -= item['amount']
            expected[item['destination']] += item['amount']
    final = {account: balance(account)[0] for account in tokens}
    assert final == expected, (final, expected)
    c_replicas = {balance('A1750')[1] for _ in range(12)}
    assert c_replicas == {'c1', 'c2'}, c_replicas
    counts = {db: int(sql(db, 'SELECT count(*) FROM bank_accounts').stdout)
              for db in ('db-a', 'db-b', 'db-c')}
    assert counts == {'db-a': 1000, 'db-b': 500, 'db-c': 500}, counts
    unresolved = int(sql('db-coordinator', "SELECT count(*) FROM bank_transactions WHERE status<>'COMPLETED'").stdout)
    assert unresolved == 0, unresolved
    prepared = {db: int(sql(db, 'SELECT count(*) FROM pg_prepared_xacts WHERE database=current_database()').stdout)
                for db in ('db-a', 'db-b', 'db-c')}
    assert all(value == 0 for value in prepared.values()), prepared
    print('PASS banking checks: balances=', json.dumps(final),
          'account counts=', json.dumps(counts),
          'unresolved=', unresolved, 'prepared=', json.dumps(prepared), flush=True)
    stages.pause('verify a repeated DONE worker and application restart persistence')
    # A fresh worker after DONE is harmless, and process restarts retain epoch 2.
    result = compose('run', '--pull', 'never', '--rm', '--no-deps', '-e', 'MIGRATION_CRASH_AT=', 'reshard')
    assert result.returncode == 0
    compose('restart', 'gateway', 'shard-b1', 'shard-b2', 'shard-c1', 'shard-c2')
    wait_ready()
    assert {account: balance(account)[0] for account in tokens} == final
    assert sql('db-coordinator', 'SELECT epoch FROM bank_routing').stdout.strip() == '2'
    assert sql('db-b', "SELECT count(*) FROM bank_accounts WHERE id>='A1501'").stdout.strip() == '0'
    evidence = {'timestamp_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                'project': project, 'checkpoints': checkpoints, 'initial_balances': initial,
                'expected_balances': expected, 'final_balances': final,
                'operations': operations, 'account_counts': counts,
                'prepared_transactions': prepared, 'unresolved_transactions': unresolved,
                'c_replicas': sorted(c_replicas), 'restart_persistence': True}
    (args.evidence / 'results.json').write_text(json.dumps(evidence, indent=2) + '\n')
    logs = compose('logs', '--no-color')
    (args.evidence / 'compose.log').write_text(logs.stdout)
    show_demo_inspection(stages, compose, args.evidence)
    print('PASS final: exact balances, idempotent replay, both C replicas, no old accounts or orphan prepares, restart persistence', flush=True)
    show_results(evidence, args.evidence)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(f'FAIL Bonus 1: {error}', file=sys.stderr, flush=True)
        raise
