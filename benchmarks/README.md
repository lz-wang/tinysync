# Benchmarks

性能基准的录制与对比。目标：优化前后数据**同硬件、同 Go 版本、同
benchmark 文件版本**可比，作为优化决策依据；wall-clock 指标不作为
CI 门禁（见 `make hardening` 的注释与 `internal/*/benchmark_test.go`
中的确定性请求计数指标）。

## 目录结构

```text
benchmarks/
├── README.md            # 本文件
├── results/             # 录制的原始结果（提交进仓库）
│   ├── <name>.bench     # 标准 go test benchmark 输出，benchstat 可直接消费
│   └── <name>.meta      # 录制环境 metadata（key=value）
└── comparisons/         # benchstat 对比输出（提交进仓库）
    └── <base>-vs-<new>.txt
```

## `.bench` 格式

标准 `go test -bench` 输出，不含任何自定义内容，保证
`benchstat before.bench after.bench` 可直接使用。

## `.meta` 格式

```text
commit=<录制时 HEAD 完整 SHA>
dirty=false
date=<UTC ISO8601>
go_version=go1.x.x
goos=<GOOS>
goarch=<GOARCH>
cpu=<CPU 型号；获取失败留空>
count=<采样次数>
benchtime=<每 benchmark 时长>
```

刻意不记录 hostname、用户名、HOME 与绝对工作目录：机器标识不进仓库。

## 录制流程

```bash
# 1. tracked 工作树必须干净（未跟踪文件不影响）——.bench 必须能
#    准确对应一个 commit SHA。
make benchmark-record NAME=p1-before

# 2. 完成优化后（同一台机器）：
make benchmark-record NAME=p1-after

# 3. 对比：
make benchmark-compare \
    BASE=benchmarks/results/p1-before.bench \
    NEW=benchmarks/results/p1-after.bench
# 输出 -> benchmarks/comparisons/p1-before-vs-p1-after.txt
```

参数默认 `BENCH_COUNT=5`、`BENCH_TIME=1s`（每个 benchmark 至少 5 个
样本供 benchstat 做统计检验）；benchstat 版本固定于 `Makefile` 的
`BENCHSTAT_VERSION`。

## 指标解读

- **确定性算法指标**（PROPFIND/op、listobjects/op、单测断言的
  ReadDir 次数）：协议请求规模是算法属性，优化目标是把它从「目录数
  主导」降为「页数主导」；这类指标同时是单元测试断言。
- **wall-clock 指标**（ns/op、B/op、allocs/op）：仅用于记录与
  benchstat 对比，不作硬阈值。
