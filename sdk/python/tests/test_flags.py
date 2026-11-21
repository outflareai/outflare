import json
from pathlib import Path

import httpx
import pytest

from outflare import LocalFlags, Outflare, OutflareError, decide_flag, decide_flags, flag_bucket, flag_device

DATA = Path(__file__).resolve().parents[3] / "engine" / "internal" / "flags" / "testdata"
BRAND = "22222222-2222-4222-8222-222222222222"

DEFS = {
    "refresh_seconds": 30,
    "flags": [
        {
            "key": "hero",
            "variants": [
                {"key": "control", "weight": 50},
                {"key": "faster", "weight": 50, "payload": {"headline": "Ship faster"}},
            ],
            "rollout": 100,
            "rules": [],
            "salt": "s1",
            "group": "",
            "group_from": 0,
            "group_to": 0,
        },
        {
            "key": "beta",
            "variants": [{"key": "on", "weight": 100}],
            "rollout": 100,
            "rules": [{"attribute": "property.plan", "op": "is", "values": ["pro"]}],
            "salt": "s2",
            "group": "",
            "group_from": 0,
            "group_to": 0,
        },
    ],
}


def test_decides_every_vector_as_the_engine_does():
    vectors = json.loads((DATA / "vectors.json").read_text())
    assert len(vectors) > 200
    for v in vectors:
        got = decide_flag(v["flag"], v["visitor"])
        assert (got["variant"], got["reason"]) == (v["variant"], v["reason"]), v["name"]


def test_reads_a_device_as_the_engine_does():
    for d in json.loads((DATA / "devices.json").read_text()):
        assert flag_device(d["user_agent"]) == d["device"], d["user_agent"]


def test_bucket_is_sha256_head_mod_10000():
    assert flag_bucket("") == 0xE3B0C44298FC1C14 % 10000
    assert flag_bucket("abc") == 0xBA7816BF8F01CFEA % 10000


def test_decide_flags_gives_the_shape_of_evaluate():
    out = decide_flags(
        DEFS, anonymous_id=" visitor-1 ", user_id="u1", properties={"plan": "Pro", "seats": 3, "none": None}
    )
    assert out["flags"]["hero"] in ("control", "faster")
    assert out["flags"]["beta"] == "on"
    assert out["reasons"] == {"hero": "match", "beta": "match"}
    anon = decide_flags(DEFS["flags"], anonymous_id="visitor-1", keys=["beta"])
    assert anon == {"flags": {"beta": False}, "payloads": {}, "reasons": {"beta": "rules"}}
    assert decide_flags(DEFS, anonymous_id="visitor-1")["flags"]["hero"] == out["flags"]["hero"]
    with pytest.raises(ValueError):
        decide_flags(DEFS)


def test_local_flags_loads_once_and_keeps_deciding_when_a_refresh_fails():
    state = {"fail": False, "body": DEFS, "calls": 0}

    def handler(request):
        state["calls"] += 1
        assert request.url.path == f"/v1/brands/{BRAND}/flags/definitions"
        if state["fail"]:
            return httpx.Response(500, json={"error": {"code": "internal", "message": "down", "request_id": "r"}})
        return httpx.Response(200, json=state["body"])

    client = Outflare(
        "oft_test_not_a_real_token",
        base_url="http://test",
        http_client=httpx.Client(transport=httpx.MockTransport(handler)),
        max_retries=0,
    )
    errors = []
    flags = LocalFlags(client, brand=BRAND, refresh_seconds=5, on_error=errors.append)
    assert flags.current() is None
    first = flags.decide(anonymous_id="visitor-1")
    flags.decide(anonymous_id="visitor-2")
    assert state["calls"] == 1, "fresh definitions cost no request"

    # One flag at a time, from the same memory.
    assert flags.variant("hero", anonymous_id="visitor-1") == first["flags"]["hero"]
    assert flags.is_enabled("hero", anonymous_id="visitor-1") is True
    assert flags.payload("hero", anonymous_id="visitor-1") == first["payloads"].get("hero")
    assert flags.variant("no-such-flag", anonymous_id="visitor-1") is False
    assert flags.is_enabled("no-such-flag", anonymous_id="visitor-1") is False
    assert flags.payload("no-such-flag", anonymous_id="visitor-1") is None
    assert state["calls"] == 1

    # Stale and Outflare is down: the page still gets its answer.
    flags._loaded_at -= 10
    state["fail"] = True
    assert flags.decide(anonymous_id="visitor-1")["flags"]["hero"] == first["flags"]["hero"]
    assert state["calls"] == 2 and len(errors) == 1
    flags.decide(anonymous_id="visitor-1")
    assert state["calls"] == 2, "a failed refresh waits for the refresh time"

    # A changed flag arrives with the next refresh.
    state["fail"] = False
    state["body"] = {"refresh_seconds": 30, "flags": []}
    flags._loaded_at -= 10
    assert flags.decide(anonymous_id="visitor-1")["flags"] == {}


def test_local_flags_fails_the_first_decide_when_definitions_cannot_be_read():
    def handler(request):
        return httpx.Response(403, json={"error": {"code": "forbidden", "message": "no", "request_id": "r"}})

    client = Outflare(
        "oft_test_not_a_real_token",
        base_url="http://test",
        http_client=httpx.Client(transport=httpx.MockTransport(handler)),
        max_retries=0,
    )
    with pytest.raises(OutflareError):
        LocalFlags(client, brand=BRAND).decide(anonymous_id="v")
