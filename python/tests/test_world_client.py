"""门 G18 的 Python 一侧：薄客户端的单元测试。

跨语言的证据由 ``scripts/verify.sh`` 的 G18 跑真进程产出；这里只把客户端的两个"不
寻常"部件钉住，好让它们在 CI 里也能被机器检查：

1. ``go_g_format`` —— 复刻 Go 的 ``strconv.FormatFloat(v,'g',-1,64)``。spec.sha256 要求
   两种语言对同一批数字得到同一段字节，所以这条规则必须独立重实现并逐值比对；
2. ``read_trace`` / ``_value_slice`` —— 原始字节的**切片**提取（绝不 ``json.dumps``
   重序列化），以及握手自校验的每一条判据。

从仓库根目录运行：``python3 -m pytest python/tests -q``
"""

from __future__ import annotations

import hashlib
import json

import pytest

import world_client

# Go 侧实测输出（strconv.FormatFloat(v, 'g', -1, 64)）—— 见内部报告里的探针清单：
# 指数形式当且仅当十进制指数 < -4 或 >= 6。
GO_FORMATTED = [
    (0.0, "0"),
    (1.0, "1"),
    (-1.0, "-1"),
    (0.1, "0.1"),
    (-1.2, "-1.2"),
    (1.0e4, "10000"),
    (9.9e5, "990000"),
    (1.0e6, "1e+06"),
    (999999.0, "999999"),
    (2.5e6, "2.5e+06"),
    (1.0e8, "1e+08"),
    (123456789.0, "1.23456789e+08"),
    (1.791703035e12, "1.791703035e+12"),
    (1.0e-4, "0.0001"),
    (1.0e-5, "1e-05"),
    (1.0e-6, "1e-06"),
    (3.536386, "3.536386"),
    (0.780886, "0.780886"),
    (0.2905708160753513, "0.2905708160753513"),
    (3.141592653589793, "3.141592653589793"),
    (1.0e20, "1e+20"),
    (1.0e21, "1e+21"),
    (5e-324, "5e-324"),
    (1.7976931348623157e308, "1.7976931348623157e+308"),
]


@pytest.mark.parametrize("value,want", GO_FORMATTED)
def test_go_g_format_matches_go(value, want):
    assert world_client.go_g_format(value) == want
    # 判据是逐位往返，不是"看起来一样"。
    assert float(world_client.go_g_format(value)) == value


def test_go_g_format_refuses_non_finite():
    for v in (float("nan"), float("inf"), float("-inf")):
        with pytest.raises(ValueError):
            world_client.go_g_format(v)


# hello 里那段规范 JSON 与它的 sha256。同一对字面量在 Go 侧也钉着
# (internal/world/world_test.go: TestHelloHashIsStableAndPinned)，因此两边的规范
# 序列化只要有一边改动，两边的测试都会红。
CANONICAL_SPEC = (
    '{"lower":[0.1,0.1,0.1,0.1,-1.2,-1.2,-1.2,-1.2,10000,10000,10000,10000],'
    '"n_coils":4,"n_params":12,'
    '"upper":[1,1,1,1,1.2,1.2,1.2,1.2,2.5e+06,2.5e+06,2.5e+06,2.5e+06]}'
)
CANONICAL_SHA256 = "7cbf60e8eae081781bd7bdd2f204f57805049b7e1a27139608aa99a94179e2b3"


def _spec():
    return {
        "lower": [0.1] * 4 + [-1.2] * 4 + [1.0e4] * 4,
        "upper": [1.0] * 4 + [1.2] * 4 + [2.5e6] * 4,
        "n_coils": 4,
        "n_params": 12,
        "sha256": CANONICAL_SHA256,
    }


def test_canonical_spec_json_matches_the_go_pinned_bytes():
    spec = _spec()
    assert world_client.canonical_spec_json(spec) == CANONICAL_SPEC
    assert world_client.spec_sha256(spec) == CANONICAL_SHA256
    assert hashlib.sha256(CANONICAL_SPEC.encode()).hexdigest() == CANONICAL_SHA256


def _hello(**over):
    hello = {
        "ok": True,
        "protocol": 1,
        "action_dim": 12,
        "observation_dim": 19,
        "obs_metric_keys": ["B_mid_T", "B_throat_T", "mirror_ratio", "volume_good",
                            "ripple", "B_coil_max_T", "cost_proxy"],
        "obs_metric_refs": [1.0, 1.0, 1.0, 1.0, 0.1, 12.0, 1.0],
        "max_steps": 20,
        "delta_scale": 0.15,
        "spec": _spec(),
        "engine": {"version": "0.1.0"},
    }
    for k, v in over.items():
        if v is None:
            hello.pop(k, None)
        else:
            hello[k] = v
    return hello


def test_handshake_accepts_a_well_formed_hello():
    assert world_client.check_handshake(_hello(), "test") == []


def test_handshake_rejects_every_field_it_claims_to_check():
    cases = {
        "action_dim off by one": _hello(action_dim=13),
        "observation_dim inconsistent": _hello(observation_dim=18),
        "missing metric refs": _hello(obs_metric_refs=None),
        "refs length mismatch": _hello(obs_metric_refs=[1.0, 1.0]),
        "a zero ref": _hello(obs_metric_refs=[1.0, 1.0, 1.0, 1.0, 0.0, 12.0, 1.0]),
        "spec hash flipped": _hello(spec=dict(_spec(), sha256="0" + CANONICAL_SHA256[1:])),
        "spec hash missing": _hello(spec={k: v for k, v in _spec().items() if k != "sha256"}),
        "lower/upper length mismatch": _hello(spec=dict(_spec(), upper=[1.0, 1.2])),
        "another protocol": _hello(protocol=2),
        "not ok": _hello(ok=False),
        "missing engine": _hello(engine=None),
        "zero max_steps": _hello(max_steps=0),
    }
    for name, hello in cases.items():
        errors = world_client.check_handshake(hello, "test")
        assert errors, f"{name}: the handshake accepted a broken hello"


def test_spec_hash_is_recomputed_not_trusted():
    """哈希必须来自复算：把 lower 改一个数字, 旧哈希立刻不成立。"""
    spec = _spec()
    spec["lower"][0] = 0.2
    errors = world_client.check_handshake(_hello(spec=spec), "test")
    assert any("spec.sha256" in e for e in errors), errors


def test_value_slice_returns_the_original_bytes():
    """取值必须是切片：行里的空格、键序、数字写法一个字节都不许变。"""
    line = '{"resp" : {"ok":true,"x":[1e+06, -0.0e0 ]} , "req":{"op":"hello"}}'
    req, pos = world_client._value_slice(line, "req")
    resp, _ = world_client._value_slice(line, "resp")
    assert req == '{"op":"hello"}'
    assert resp == '{"ok":true,"x":[1e+06, -0.0e0 ]}'
    assert line[pos - len(req):pos] == req


def test_read_trace_loses_no_bytes():
    trace = world_client.REPO_ROOT / "testdata" / "world_trace_golden.jsonl"
    lines = world_client.read_trace(trace)
    assert len(lines) == 8, "the golden trace is hello + reset + 5 steps + close"
    raw = trace.read_text(encoding="utf-8").splitlines()
    for line, original in zip(lines, raw):
        assert f'{{"req":{line.req},"resp":{line.resp}}}' == original
        json.loads(line.req)  # 请求本身仍然是一个 JSON 对象
        json.loads(line.resp)
    assert json.loads(lines[0].req) == {"op": "hello"}
    assert json.loads(lines[-1].req) == {"op": "close"}


def test_read_trace_refuses_an_empty_line(tmp_path):
    path = tmp_path / "t.jsonl"
    path.write_text('{"req":{"op":"hello"},"resp":{"ok":true}}\n\n', encoding="utf-8")
    with pytest.raises(SystemExit):
        world_client.read_trace(path)


def test_first_diff_points_at_the_first_differing_byte():
    assert world_client.first_diff("abcdef", "abcxef") == 3
    assert world_client.first_diff("abc", "abcdef") == 3
