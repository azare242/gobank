"""Regression checks for interpreting SQL evidence, including same-shard rows."""
import unittest

from verify import verify


def evidence():
    clusters = {}
    for name in ("shard-a-db", "shard-b-db", "coordinator-db"):
        clusters[name] = {
            "roles": {"primary": "f", "standby": "t"},
            "replication": [{"state": "streaming", "sync_state": "quorum"}],
            "prepared": [],
            "participants": [{"id": "cross", "state": "COMMIT"}],
        }
    clusters["coordinator-db"]["transactions"] = [
        {"id": "cross", "status": "COMPLETED", "decision": "COMMIT",
         "participants": [{"shard_id": "a"}, {"shard_id": "b"}], "acks": ["a", "b"]},
        {"id": "local", "status": "COMPLETED", "decision": "COMMIT",
         "participants": [{"shard_id": "a"}], "acks": []},
    ]
    return clusters


class EvidenceTests(unittest.TestCase):
    def test_local_mutation_does_not_require_two_phase_participant(self):
        verify(evidence())

    def test_partial_cross_shard_commit_is_rejected(self):
        data = evidence()
        data["shard-b-db"]["participants"][0]["state"] = "ABORT"
        with self.assertRaisesRegex(AssertionError, "participant decision mismatch"):
            verify(data)

    def test_missing_acknowledgment_is_rejected(self):
        data = evidence()
        data["coordinator-db"]["transactions"][0]["acks"] = ["a"]
        with self.assertRaisesRegex(AssertionError, "missing acknowledgment"):
            verify(data)

    def test_unresolved_prepared_transaction_is_rejected(self):
        data = evidence()
        data["shard-a-db"]["prepared"] = [{"gid": "unresolved"}]
        with self.assertRaisesRegex(AssertionError, "unresolved prepared"):
            verify(data)


if __name__ == "__main__":
    unittest.main()
