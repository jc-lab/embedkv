# embedkv 프로젝트 구조

`embedkv`는 [ARCH.md](ARCH.md)에 정의된 고정 크기 블록 기반 내장형 key-value 스토리지이다.
동일한 on-disk 포맷을 **Rust**와 **Go** 두 언어로 구현한다. 두 구현은 같은 storage 바이너리 포맷(little-endian, packed layout, IEEE CRC-32)을 사용하므로, 한쪽이 기록한 storage를 다른 쪽이 읽을 수 있어야 한다.

---

## 1. 디렉터리 레이아웃

구현 소스는 언어별 디렉터리(`rust/`, `go/`)에 두되, 외부에서 의존성으로 바로 가져다 쓸 수 있도록 **모듈/패키지 매니페스트는 루트**에 둔다.

```text
embedkv/
├── ARCH.md                # 아키텍처 스펙 (단일 진실 공급원)
├── PROJECT.md             # 본 문서
├── LICENSE
│
├── go.mod                 # module github.com/jc-lab/embedkv   (루트)
├── go/                    # Go 구현 (package embedkv)
│   ├── embedkv.go
│   ├── store.go, block.go, ...
│   └── *_test.go
│
├── Cargo.toml             # [workspace] members = ["rust"]      (루트)
├── rust/                  # Rust 구현 (crate embedkv)
│   ├── Cargo.toml         # [package] name = "embedkv"
│   ├── src/
│   └── tests/
│
└── testdata/              # 두 구현이 공유하는 cross-language 포맷 fixture
    ├── small_value.bin
    ├── large_value.bin
    └── recovery/*.bin
```

### 외부에서 사용하는 방법

- **Go**: 루트 `go.mod`의 module path는 `github.com/jc-lab/embedkv`. Go 코드는 `go/`에 있으므로 import 경로는 다음과 같다.

  ```go
  import "github.com/jc-lab/embedkv/go"   // package embedkv
  // embedkv.Open(...)
  ```

- **Rust**: 루트 `Cargo.toml`은 workspace이고 실제 crate는 `rust/`에 있다. git 의존성으로 가져올 때 cargo가 workspace member를 해석한다.

  ```toml
  embedkv = { git = "https://github.com/jc-lab/embedkv", package = "embedkv" }
  ```

`ARCH.md`가 포맷의 단일 진실 공급원이며, `testdata/`의 바이너리 fixture로 Rust/Go 양쪽의 호환성을 교차 검증한다. 두 구현은 동일한 little-endian / packed / IEEE CRC-32 포맷을 사용하므로 한쪽이 기록한 storage를 다른 쪽이 그대로 읽을 수 있어야 한다(바이너리 호환).

---

## 2. 모듈 구성

두 언어 모두 스펙(ARCH.md)의 절을 따라 동일한 책임 단위로 나눈다.

| 모듈            | 책임                                            | ARCH 절 |
| ------------- | --------------------------------------------- | ------ |
| `format`      | 상수, byte order, block type, 구조체 offset/size   | §3, §5 |
| `crc`         | IEEE CRC-32 계산 (block 끝 4바이트 crc 필드를 0으로 간주)   | §3.4   |
| `block`       | StorageHeader / RecordDescriptor / ValueChunk / FreeBlock 직렬화·역직렬화 | §6–§9  |
| `device`      | 고정 크기 block read/write/flush 추상화 (file, memory backend) | §15    |
| `record`      | record 분할(descriptor payload + value chunks), 조립, 완전성 검증 | §7, §10, §12, §19 |
| `store`       | open / get / put / delete / update (copy-on-write), 여러 replica device fan-out·교차 선택 | §11–§16, §20 |
| `recovery`    | 전체 scan, garbage 분류·제거, 최신 완전 record 선택       | §17, §18 |
| `index`       | (선택) 메모리 key → descriptor 인덱스                | §21    |

### 2.1 Rust (`rust/src/`)

```text
lib.rs            # 공개 API 재노출
format.rs         # 상수 및 layout 정의
crc.rs
block/            # mod.rs, header.rs, descriptor.rs, chunk.rs, free.rs
device.rs         # BlockDevice trait + FileDevice, MemDevice
record.rs
store.rs          # Store 타입(여러 replica device), get/put/delete/update
recovery.rs
index.rs
error.rs          # Error/Result 타입
```

### 2.2 Go (`go/`)

```text
embedkv.go        # 공개 API (Store, Open 등)
format.go         # 상수 및 layout 정의
crc.go
block.go          # 또는 block/ 패키지: header.go, descriptor.go, chunk.go, free.go
device.go         # BlockDevice 인터페이스 + FileDevice, MemDevice
record.go
store.go          # Store 타입(여러 replica device), get/put/delete/update
recovery.go
index.go
errors.go
```

---

## 3. 공개 API (양 언어 공통 개념)

```text
Open(devices, options)       # 각 replica의 storage header 검증 후 store 핸들 반환
Format(devices, options)     # 새 storage 초기화 (각 device에 header 작성)
Get(key)        -> value     # replica들 중 가장 높은 완전 generation 반환
Put(key, value)              # 모든 replica에 신규 write 또는 update(copy-on-write)
Delete(key)                  # 모든 replica에서 제거
Recover()                    # 각 replica를 독립적으로 scan, garbage 제거
```

`Open`/`Format`은 하나 이상의 replica `BlockDevice` 목록을 받는다. 단일 device 사용은 1개짜리 목록을 넘기는 경우이며, write는 모든 replica로 fan-out되고 read는 replica 간 최신 완전 record를 선택한다(§20).

---

## 4. 테스트 전략

- **단위 테스트**: 각 모듈(crc, block 직렬화, record 분할/조립, 완전성 판정)을 언어 내부에서 검증한다.
- **시나리오 테스트**: ARCH.md §23 예시(작은/큰 value, update 중 전원 차단, record 제거 중 전원 차단)를 메모리 backend로 재현한다.
- **Cross-language 호환성**: `testdata/`의 fixture를 Rust가 쓰고 Go가 읽기 / 반대 방향 모두 검증한다.
- **Recovery/fault injection**: 부분 write·flush 누락을 흉내 내는 device wrapper로 복구 동작을 확인한다.

---

## 5. 구현 순서 제안

1. `format` + `crc` — 상수와 CRC를 먼저 고정한다.
2. `block` 직렬화/역직렬화 + round-trip 테스트.
3. `device` 추상화(MemDevice부터).
4. `record` 분할/조립/완전성 검증.
5. `store`의 get/put(신규 write).
6. `recovery`(scan + garbage 제거).
7. update(copy-on-write) + delete.
8. `index`, 여러 replica device fan-out·교차 선택(§20).
9. Cross-language fixture 교차 검증.
