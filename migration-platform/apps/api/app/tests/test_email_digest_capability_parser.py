"""Offline tests for the pure digest-CAPABILITY parser.

Fully offline: every test passes a RAW fixture string to a pure function. There
is NO network, NO socket, NO SSH, NO IMAP/POP3/SMTP/ManageSieve connection, NO
cPanel/UAPI, NO shadow access. Fixtures are synthetic (no real hosts/secrets).
"""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path


def _load():
    root = Path(__file__).resolve().parents[4]
    path = root / "scripts" / "email_digest_capability_parser.py"
    spec = importlib.util.spec_from_file_location("email_digest_capability_parser", path)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod


mod = _load()
parse = mod.parse_capability
PASS = mod.PASS_PASSWORD_AUTH_ONLY
FAIL = mod.FAIL_DIGEST_OR_CHALLENGE_RESPONSE_OFFERED
INCONCLUSIVE = mod.INCONCLUSIVE


# --------------------------------------------------------------------------- #
# 1-3  IMAP
# --------------------------------------------------------------------------- #
def test_1_imap_plain_login_passes():
    r = parse("imap", "* CAPABILITY IMAP4rev1 STARTTLS AUTH=PLAIN AUTH=LOGIN\r\n* OK")
    assert r.decision == PASS
    assert r.digest_out_of_scope_required is False
    assert set(r.safe_password_mechanisms) == {"PLAIN", "LOGIN"}
    assert r.risky_mechanisms == []


def test_2_imap_cram_md5_fails():
    r = parse("imap", "* CAPABILITY IMAP4rev1 AUTH=PLAIN AUTH=CRAM-MD5")
    assert r.decision == FAIL
    assert r.digest_out_of_scope_required is True
    assert "CRAM-MD5" in r.risky_mechanisms


def test_3_imap_digest_md5_fails():
    r = parse("imap", "* CAPABILITY IMAP4rev1 AUTH=LOGIN AUTH=DIGEST-MD5")
    assert r.decision == FAIL
    assert "DIGEST-MD5" in r.risky_mechanisms


# --------------------------------------------------------------------------- #
# 4-6  POP3 (CAPA SASL line + greeting APOP)
# --------------------------------------------------------------------------- #
def test_4_pop3_capa_plain_login_passes():
    r = parse("pop3", "+OK Capability list follows\r\nUSER\r\nSASL PLAIN LOGIN\r\n.\r\n")
    assert r.decision == PASS
    assert set(r.safe_password_mechanisms) == {"PLAIN", "LOGIN"}


def test_5_pop3_capa_cram_md5_fails():
    r = parse("pop3", "+OK Capability list follows\r\nSASL PLAIN LOGIN CRAM-MD5\r\n.\r\n")
    assert r.decision == FAIL
    assert "CRAM-MD5" in r.risky_mechanisms


def test_6_pop3_greeting_apop_challenge_fails():
    # the greeting carries a <pid.clock@host> APOP challenge -> APOP supported
    greeting = "+OK POP3 server ready <1896.697170952@mail.example.test>\r\n"
    r = parse("pop3", greeting)
    assert r.decision == FAIL
    assert "APOP" in r.risky_mechanisms


# --------------------------------------------------------------------------- #
# 7-8  SMTP submission EHLO
# --------------------------------------------------------------------------- #
def test_7_smtp_ehlo_plain_login_passes():
    ehlo = ("250-mail.example.test Hello\r\n"
            "250-SIZE 20480000\r\n"
            "250-AUTH PLAIN LOGIN\r\n"
            "250 STARTTLS\r\n")
    r = parse("smtp", ehlo)
    assert r.decision == PASS
    assert set(r.safe_password_mechanisms) == {"PLAIN", "LOGIN"}


def test_8_smtp_ehlo_with_cram_md5_fails_both_forms():
    space_form = "250-AUTH PLAIN LOGIN CRAM-MD5\r\n250 OK\r\n"
    eq_form = "250-AUTH=PLAIN LOGIN CRAM-MD5\r\n250 OK\r\n"
    for ehlo in (space_form, eq_form):
        r = parse("smtp", ehlo)
        assert r.decision == FAIL
        assert "CRAM-MD5" in r.risky_mechanisms


# --------------------------------------------------------------------------- #
# 9  SCRAM-* on any service
# --------------------------------------------------------------------------- #
def test_9_scram_any_service_fails():
    assert parse("imap", "* CAPABILITY AUTH=SCRAM-SHA-256").decision == FAIL
    assert parse("smtp", "250 AUTH PLAIN SCRAM-SHA-1").decision == FAIL
    r = parse("pop3", "+OK\r\nSASL PLAIN SCRAM-SHA-256-PLUS\r\n.\r\n")
    assert r.decision == FAIL
    assert any(m.startswith("SCRAM-") for m in r.risky_mechanisms)


# --------------------------------------------------------------------------- #
# 10  Empty / malformed -> INCONCLUSIVE
# --------------------------------------------------------------------------- #
def test_10_empty_and_malformed_inconclusive():
    assert parse("imap", "").decision == INCONCLUSIVE
    assert parse("imap", "garbage without mechanisms").decision == INCONCLUSIVE
    assert parse("smtp", "250 STARTTLS\r\n").decision == INCONCLUSIVE  # no AUTH line
    # only unrelated (token/cert) mechs, no PLAIN/LOGIN, no risky -> inconclusive
    assert parse("imap", "* CAPABILITY AUTH=EXTERNAL AUTH=GSSAPI").decision == INCONCLUSIVE


# --------------------------------------------------------------------------- #
# 11  Robustness: mixed case, extra spaces, odd multiline
# --------------------------------------------------------------------------- #
def test_11_parser_robust_mixed_case_and_spacing():
    r = parse("imap", "* capability   imap4rev1    auth=plain\r\n   AUTH=Login  ")
    assert r.decision == PASS
    assert set(r.safe_password_mechanisms) == {"PLAIN", "LOGIN"}
    r2 = parse("smtp", "250-auth   plain   login   cram-md5\r\n")
    assert r2.decision == FAIL and "CRAM-MD5" in r2.risky_mechanisms
    r3 = parse("managesieve", '"SASL" "PLAIN   LOGIN"\r\n"SIEVE" "fileinto"\r\n')
    assert r3.decision == PASS


# --------------------------------------------------------------------------- #
# 12  No leak: raw greeting host / secrets never appear in the result
# --------------------------------------------------------------------------- #
def test_12_no_leak_of_raw_text_or_host():
    greeting = "+OK POP3 ready <9999.111@secret-internal-host.corp.example>\r\n"
    r = parse("pop3", greeting)
    blob = json.dumps(r.to_dict())
    assert "secret-internal-host.corp.example" not in blob
    assert "9999.111" not in blob
    assert "<" not in blob and ">" not in blob  # no raw challenge token echoed
    assert r.decision == FAIL and "APOP" in r.risky_mechanisms


# --------------------------------------------------------------------------- #
# Extras: unknown service, dedupe, aggregate
# --------------------------------------------------------------------------- #
def test_unknown_service_is_inconclusive():
    r = parse("ftp", "whatever AUTH=PLAIN")
    assert r.decision == INCONCLUSIVE
    assert r.service == "ftp"


def test_mechanisms_deduped_and_uppercased():
    r = parse("imap", "* CAPABILITY auth=plain AUTH=PLAIN AUTH=Plain AUTH=LOGIN")
    assert r.mechanisms_offered == ["PLAIN", "LOGIN"]  # deduped, uppercased, ordered


def test_aggregate_fail_wins():
    imap = parse("imap", "* CAPABILITY AUTH=PLAIN AUTH=LOGIN")   # PASS
    pop3 = parse("pop3", "+OK\r\nSASL PLAIN LOGIN CRAM-MD5\r\n.\r\n")  # FAIL
    agg = mod.aggregate([imap, pop3])
    assert agg["overall_decision"] == FAIL
    assert agg["digest_out_of_scope_required"] is True
    assert "pop3" in agg["risky_by_service"]


def test_aggregate_all_pass():
    imap = parse("imap", "* CAPABILITY AUTH=PLAIN")
    smtp = parse("smtp", "250 AUTH PLAIN LOGIN\r\n")
    agg = mod.aggregate([imap, smtp])
    assert agg["overall_decision"] == PASS
    assert agg["digest_out_of_scope_required"] is False


def test_aggregate_inconclusive_when_no_pass_no_fail():
    a = parse("imap", "")
    b = parse("smtp", "250 STARTTLS\r\n")
    agg = mod.aggregate([a, b])
    assert agg["overall_decision"] == INCONCLUSIVE
