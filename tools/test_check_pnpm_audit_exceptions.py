import unittest

from check_pnpm_audit_exceptions import validate_audit


class AuditResponseTests(unittest.TestCase):
    # 验证审计失败和不完整响应不会被误判为通过。
    def test_registry_error_is_rejected(self):
        self.assertTrue(validate_audit({"error": {"code": "ECONNRESET"}}))

    def test_missing_counts_are_rejected(self):
        self.assertTrue(validate_audit({"advisories": {}}))

    def test_invalid_counts_are_rejected(self):
        for count in ("0", -1, True):
            with self.subTest(count=count):
                self.assertTrue(validate_audit({
                    "metadata": {"vulnerabilities": {"high": count}},
                    "advisories": {},
                }))

    def test_missing_details_are_rejected(self):
        self.assertTrue(validate_audit({
            "metadata": {"vulnerabilities": {"high": 1}},
            "advisories": {},
        }))

    def test_clean_response_is_accepted(self):
        self.assertEqual(validate_audit({
            "metadata": {"vulnerabilities": {"high": 0}},
            "advisories": {},
        }), [])

    def test_findings_response_is_accepted(self):
        self.assertEqual(validate_audit({
            "metadata": {"vulnerabilities": {"high": 1}},
            "advisories": {"123": {"module_name": "axios", "severity": "high"}},
        }), [])


if __name__ == "__main__":
    unittest.main()
