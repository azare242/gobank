#!/usr/bin/env python3
"""Bonus II real-container demo on an isolated Compose project (Linux).

Uses prebuilt shard, Gateway and Go controller images. Keeps containers, database volumes, controller
state and evidence. Stops the controller on exit. Never run on production data.
"""
import argparse
import hashlib
import concurrent.futures
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time
import uuid

import experiments as bank
from demo_stages import DemoStages
LABEL = 'io.gobank.healing.slot'


class Docker:
    """Demo-only inspection helpers; recovery runs entirely in Go/Compose."""
    def run(self, *args):
        return subprocess.run(['docker', *args], check=True, text=True,
                              capture_output=True, timeout=45).stdout

    def inspect(self, identity):
        ids = self.run('ps', '-aq', '--no-trunc').split()
        if identity not in ids:
            ids = self.run('ps', '-aq', '--no-trunc', '--filter', 'name=^/' + identity + '$').split()
            if not ids:
                return None
            identity = ids[0]
        return json.loads(self.run('inspect', identity))[0]

    def bootstrap(self, project, service):
        ids = self.run('ps', '-aq', '--no-trunc', '--filter', 'label=com.docker.compose.project=' + project,
                       '--filter', 'label=com.docker.compose.service=' + service).split()
        assert len(ids) == 1, (service, ids)
        return self.inspect(ids[0])

ROOT = Path(__file__).resolve().parents[2]


def show_demo_inspection(stages, compose, docker, project, evidence_dir):
    """Display command-sheet blocks 8--14 with an Enter prompt for each."""
    def show_compose(*parts):
        print('$ compose ' + ' '.join(parts), flush=True)
        print(compose(*parts).rstrip(), flush=True)

    def show_sql(db, query):
        show_compose('exec', '-T', db, 'psql', '-v', 'ON_ERROR_STOP=1',
                     '-U', 'bank', '-d', 'bank', '-c', query)

    stages.pause('Block 8: show the saved summary and controller crash/recovery logs')
    summary = json.loads((evidence_dir / 'summary.json').read_text())
    print(json.dumps(summary, indent=2), flush=True)
    for name in ('crashed-controller.log', 'controller.log'):
        print(f'{name}:', flush=True)
        print((evidence_dir / name).read_text().rstrip(), flush=True)

    stages.pause('Block 9: review the verified recovery checks')
    fields = ('result', 'controller_crash_exit', 'unresponsive_replica',
              'healthy_originals_preserved', 'gateway_replicas', 'orphan_prepares')
    print(json.dumps({key: summary[key] for key in fields}, indent=2), flush=True)

    stages.pause('Block 10: compare expected and actual balances using this run\'s counts')
    expected = [10000 + summary['counts']['deposits'], 10000]
    print(json.dumps(dict(counts=summary['counts'], retries=summary['retries'],
                          balances=summary['balances'], expected_balances=expected,
                          balances_match=summary['balances'] == expected), indent=2), flush=True)
    assert summary['balances'] == expected, (summary['balances'], expected)

    stages.pause('Block 11: read the two account balances directly from PostgreSQL')
    show_sql('db-a', "SELECT id,balance FROM bank_accounts WHERE id='A0500';")
    show_sql('db-b', "SELECT id,balance FROM bank_accounts WHERE id='A1500';")

    stages.pause('Block 12: show Compose containers and the managed A1 replacement')
    show_compose('ps', '-a')
    print(docker.run('ps', '-a', '--filter', f'label={LABEL}={project}/shard-a1',
                     '--format', 'table {{.ID}}\t{{.Names}}\t{{.Status}}').rstrip(), flush=True)

    stages.pause('Block 13: show remaining prepared transactions in both shard databases')
    for db in ('db-a', 'db-b'):
        print(db, flush=True)
        show_sql(db, 'SELECT gid,prepared FROM pg_prepared_xacts WHERE database=current_database();')

    stages.pause('Block 14: inspect controller and replacement restart policies')
    controller_id = compose('ps', '-aq', 'healing-controller').strip()
    assert controller_id, 'controller container missing'
    print(docker.run('inspect', '--format',
                     '{{.Name}} restart={{.HostConfig.RestartPolicy.Name}}',
                     controller_id, summary['final_container']).rstrip(), flush=True)


def show_results(summary, evidence_dir):
    """Display the verified recovery and banking results for the recording."""
    print('\n--- Bonus 2: visible evidence ---', flush=True)
    print('Controller crash exit:', summary['controller_crash_exit'])
    print('Crash recovery: adopted the same created container without duplicates')
    print('Adopted container:', summary['adopted_container'])
    print('Unresponsive replica:', summary['unresponsive_replica'])
    print('Final replacement:', summary['final_container'])
    print('Managed A1 replacements: 1')
    print('Healthy originals preserved:', ', '.join(summary['healthy_originals_preserved']))
    print('Gateway replicas:', ', '.join(summary['gateway_replicas']))
    print('Distinct successful operations:', json.dumps(summary['counts'], sort_keys=True))
    print('Retries:', summary['retries'])
    print('Original request keys replayed: no additional banking effects')
    print('Account balances (expected / actual):')
    expected = [10000 + summary['counts']['deposits'], 10000]
    for account, wanted, actual in zip(('A0500', 'A1500'), expected, summary['balances']):
        print(f'  {account}: {wanted} / {actual}')
    print(f"Total money (expected / actual): {sum(expected)} / {sum(summary['balances'])}")
    print('Orphan prepared transactions:', summary['orphan_prepares'])
    print('Controller stopped; banking containers and durable state retained')
    print('Evidence:', evidence_dir, flush=True)
    print('PASS Bonus 2: failed and unresponsive replicas replaced, controller '
          'recovery without duplicates, exact balances and idempotent replay', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env-file', required=True, type=Path)
    parser.add_argument('--evidence', required=True, type=Path)
    parser.add_argument('--override', type=Path)
    parser.add_argument('--url', default='http://127.0.0.1:3000')
    parser.add_argument('--no-pause', action='store_true',
                        help='Run without waiting for Enter between stages (automation).')
    args = parser.parse_args()
    stages = DemoStages(args.no_pause)
    os.umask(0o077)
    args.evidence.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    for line in args.env_file.read_text().splitlines():
        if line.strip() and not line.lstrip().startswith('#'):
            key, value = line.split('=', 1)
            env[key.strip()] = value.strip().strip('\"\'')
    project = env['COMPOSE_PROJECT_NAME']
    if not project.startswith('gobank-healing-demo'):
        parser.error('Use a dedicated COMPOSE_PROJECT_NAME starting with gobank-healing-demo')
    if (args.evidence / 'summary.json').exists():
        parser.error('Use a fresh project and evidence directory')
    command = ['docker', 'compose', '--env-file', str(args.env_file.resolve()),
               '-f', str(ROOT / 'deploy/sharded/compose.yml'),
               '-f', str(ROOT / 'deploy/sharded/self-healing.yml')]
    if args.override:
        command += ['-f', str(args.override.resolve())]
    tuning = args.evidence.resolve() / 'controller-options.yml'
    tuning.write_text('services:\n  healing-controller:\n    command: [--grace, 3s, --interval, 300ms]\n')
    command += ['-f', str(tuning)]
    def compose(*parts):
        return subprocess.run(command + list(parts), env=env, check=True,
                              capture_output=True, text=True, timeout=120).stdout
    def wait(check, description, timeout=100):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            value = check()
            if value:
                return value
            time.sleep(.3)
        raise AssertionError('Timeout: ' + description)

    stages.pause('start two shards and initialize both demo balances to 10000')
    compose('up', '-d', '--no-build', '--pull', 'never', 'gateway', 'shard-a1', 'shard-a2', 'shard-b1', 'shard-b2')
    bank.BASE = args.url
    os.environ['FIXTURE_PASSWORD'] = env['FIXTURE_PASSWORD']
    bank.ready()
    bank.set_balance('A0500', 10000)
    bank.set_balance('A1500', 10000)
    docker = Docker()
    services = ['shard-a1', 'shard-a2', 'shard-b1', 'shard-b2']
    originals = {s: docker.bootstrap(project, s)['Id'] for s in services}
    for account in ['A0500', 'A1500']:
        bank.token(account)
    print('PASS baseline: A0500=10000, A1500=10000; original replicas:',
          json.dumps(originals, sort_keys=True), flush=True)
    def start_controller(crash=False):
        env['HEALING_CRASH_AT'] = 'after-create' if crash else ''
        compose('up', '-d', '--no-build', '--pull', 'never', '--no-deps', 'healing-controller')
        return docker.bootstrap(project, 'healing-controller')['Id']
    def controller():
        return docker.bootstrap(project, 'healing-controller')
    stop = threading.Event()
    counts = {'transfers': 0, 'deposits': 0}
    retries = {'count': 0}
    def retry(fn):
        end = stages.active_time() + 100
        while True:
            stages.wait_for_traffic()
            try:
                result = fn()
                if result.get('http', 200) < 500 and result.get('status') == 'success':
                    return result
            except (OSError, ValueError):
                pass
            retries['count'] += 1
            if stages.active_time() > end:
                raise AssertionError('Bank operation did not resolve')
            time.sleep(.2)
    def transfers():
        while not stop.is_set():
            for source, dest in [('A0500', 'A1500'), ('A1500', 'A0500')]:
                key = str(uuid.uuid4())
                retry(lambda: bank.transfer(source, dest, 1, key))
                # Replay the exact request: one durable banking effect.
                retry(lambda: bank.transfer(source, dest, 1, key))
                counts['transfers'] += 1
            time.sleep(.05)
    def deposits():
        while not stop.is_set():
            key = str(uuid.uuid4())
            retry(lambda: bank.change('A0500', 1, 'deposit', key))
            retry(lambda: bank.change('A0500', 1, 'deposit', key))
            counts['deposits'] += 1
            time.sleep(.05)
    replacement_name = 'gobank-healed-' + hashlib.sha256((project + '/shard-a1').encode()).hexdigest()[:20]
    def replacement():
        return docker.inspect(replacement_name)
    def admitted():
        return bank.request('/accounts/A0500/balance', account='A0500').get('replica') == 'a1'

    try:
        stages.pause('start the controller with crash-after-create enabled')
        start_controller(crash=True)
        wait(lambda: 'controller-ready' in compose('logs', '--no-color', 'healing-controller'), 'controller bootstrap')
        print('PASS controller bootstrap: ready; crash-after-create armed', flush=True)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
            futures = [executor.submit(transfers), executor.submit(deposits)]
            try:
                stages.pause('kill original A1 under concurrent banking traffic')
                docker.run('kill', originals['shard-a1'])
                wait(lambda: controller()['State']['Status'] == 'exited', 'injected controller exit')
                assert controller()['State']['ExitCode'] == 86, controller()['State']
                (args.evidence / 'crashed-controller.log').write_text(compose('logs', '--no-color', 'healing-controller'))
                created = replacement()
                assert created and created['State']['Status'] == 'created', created
                created_id = created['Id']
                print('Controller crashed after create; restarting against durable state.', flush=True)
                print('PASS controller crash: exit=86; created replacement:', created_id, flush=True)
                stages.pause('restart the controller and adopt the same created container')
                start_controller()
                wait(lambda: (c := replacement()) and c['State']['Status'] == 'running',
                     'replacement readiness')
                assert replacement()['Id'] == created_id, 'restart created a duplicate'
                wait(admitted, 'Gateway admission')
                print('PASS adoption: same container ID; Gateway serves a1:', created_id, flush=True)
                # A paused process stays running but cannot answer HTTP probes.
                stages.pause('pause the adopted A1 and verify automatic replacement')
                docker.run('pause', created_id)
                wait(lambda: (c := replacement()) and c['Id'] != created_id and
                     c['State']['Status'] == 'running', 'second replacement')
                wait(admitted, 'second Gateway admission')
                assert controller()['State']['Status'] == 'running'
                print('PASS unresponsive recovery: new container ID; Gateway serves a1:',
                      replacement()['Id'], flush=True)
                stages.pause('finish traffic and verify balances, healthy replicas and 2PC state')
            finally:
                stop.set()
                for future in futures:
                    future.result(timeout=120)
        for service in services[1:]:
            assert docker.inspect(originals[service])['State']['Status'] == 'running', service
        matches = docker.run('ps', '-aq', '--filter', 'label=' + LABEL + '=' + project + '/shard-a1').split()
        assert len(matches) == 1, matches
        final = [bank.balance('A0500'), bank.balance('A1500')]
        assert final == [10000 + counts['deposits'], 10000], (counts, final)
        assert counts['transfers'] > 0 and counts['deposits'] > 0
        replicas = {bank.request('/accounts/A0500/balance', account='A0500')['replica'] for _ in range(20)}
        assert replicas == {'a1', 'a2'}, replicas
        for db in ['db-a', 'db-b']:
            result = compose('exec', '-T', db, 'psql', '-U', 'bank', '-d', 'bank', '-Atc',
                             'SELECT count(*) FROM pg_prepared_xacts WHERE database=current_database()')
            assert result.strip() == '0', (db, result)
        summary = dict(result='passed', counts=counts, retries=retries['count'], balances=final,
                       controller_crash_exit=86, unresponsive_replica='docker pause', adopted_container=created_id,
                       final_container=matches[0], healthy_originals_preserved=services[1:],
                       gateway_replicas=sorted(replicas), orphan_prepares=0)
        (args.evidence / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
        print(json.dumps(summary, indent=2), flush=True)
    finally:
        stop.set()
        (args.evidence / 'controller.log').write_text(compose('logs', '--no-color', 'healing-controller'))
        compose('stop', 'healing-controller')

    show_demo_inspection(stages, compose, docker, project, args.evidence)
    show_results(summary, args.evidence)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(f'FAIL Bonus 2: {error}', file=sys.stderr, flush=True)
        raise
