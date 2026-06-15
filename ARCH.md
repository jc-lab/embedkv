# embedkv 스토리지 아키텍처

## 1. 개요

`embedkv`는 고정된 크기의 스토리지 영역 위에서 동작하는 내장형 key-value 스토리지이다. 스토리지는 고정 크기 블록들의 배열로 구성되며, 모든 read/write는 블록 단위로 수행된다.

embedkv는 작은 저장 영역에서도 동작할 수 있도록 블록 메타데이터를 최소화하고, write 횟수를 줄이는 것을 중요한 설계 원칙으로 한다. 하나의 블록은 가능한 한 한 번의 write로 완성된 형태가 되어야 하며, commit을 위해 같은 블록을 다시 write하는 구조는 사용하지 않는다.

전원 차단이나 부분 쓰기가 발생하더라도, 복구 시 완전한 block들로만 구성된 record를 선택하여 읽을 수 있어야 한다.

---

## 2. 설계 목표

embedkv의 주요 설계 목표는 다음과 같다.

* 고정 크기 스토리지 영역에서 동작
* 고정 크기 블록 단위의 read/write
* 블록 헤더 및 메타데이터 최소화
* write 횟수 최소화
* 순차적 write에 적합한 layout
* block 단위 CRC32 검증
* 완전한 block들로 구성된 record만 read 대상으로 사용
* update 시 기존 데이터를 덮어쓰지 않는 copy-on-write 구조
* update 완료 후 기존 record block 제거
* 전원 차단 이후 복구 가능
* 복구 시 garbage 제거
* storage 전체 replica 기반 복구 지원

---

## 3. 기본 전제

### 3.1 Byte Order

모든 multi-byte integer는 **little-endian**으로 저장한다.

예를 들어 `u32` 값 `0x12345678`은 다음과 같이 저장된다.

```text
78 56 34 12
```

### 3.2 정렬

모든 구조체 필드는 byte 단위로 packed layout을 사용한다. 구조체 내부에는 compiler padding을 허용하지 않는다.

구현체는 반드시 명시된 offset과 size에 맞춰 serialize/deserialize 해야 한다.

### 3.3 Block Index

block index는 `u32`를 사용한다.

| 값                  | 의미                   |
| ------------------ | -------------------- |
| `0`                | storage header block |
| `1..block_count-1` | data block           |
| `0xFFFFFFFF`       | null block reference |

`0xFFFFFFFF`는 `next_chunk`가 없음을 나타낼 때 사용한다.

### 3.4 CRC32

CRC32는 IEEE CRC-32 polynomial 기준을 사용한다.

block CRC는 각 block 자체가 올바르게 기록되었는지 확인하기 위한 값이다.

`block_crc32`는 block type과 무관하게 모든 non-free block의 **마지막 4바이트**(offset `block_size - 4`)에 저장한다. type별 header에는 CRC를 두지 않는다.

CRC 계산 시 `block_size - 4` 위치의 4바이트는 `0x00000000`으로 간주한다. 계산 범위는 block 전체이다.

free block에는 CRC가 없다.

---

## 4. Storage 구조

embedkv의 하나의 storage는 고정 크기 블록들의 배열로 구성된다.

```text
Storage
+----------+----------+----------+----------+----------+
| Block 0  | Block 1  | Block 2  | Block 3  | Block N  |
+----------+----------+----------+----------+----------+
```

첫 번째 블록은 항상 `storage header`로 사용된다. 나머지 블록은 다음 block type 중 하나로 해석된다.

| Block Type        | 값                | 설명                                      |
| ----------------- | ---------------- | --------------------------------------- |
| free block        | `0x00` 또는 `0xFF` | 사용 가능한 빈 블록                             |
| storage header    | `0x01`           | storage 전체 정보를 담는 첫 번째 블록               |
| record descriptor | `0x02`           | 하나의 key-value record를 설명하는 블록           |
| value chunk       | `0x03`           | record descriptor에 포함되지 못한 나머지 value 조각 |

free block은 `0x00` 또는 `0xFF` 모두 허용한다. 이는 zero-filled format과 erased NAND 상태를 모두 자연스럽게 free 상태로 인식하기 위함이다.

---

## 5. 공통 Block 규칙

free block을 제외한 모든 block은 다음 규칙을 따른다.

1. 첫 byte는 `block_type`이다.
2. block type은 `0x01`, `0x02`, `0x03` 중 하나이다.
3. 각 block은 마지막 4바이트(`block_size - 4`)에 `block_crc32`를 가진다.
4. block CRC 검증에 실패한 block은 손상된 것으로 간주한다.
5. 손상된 block은 read 대상에서 제외하며, recovery 시 garbage로 제거한다.

개별 data block에는 storage magic이나 format version을 반복 저장하지 않는다. storage magic과 version은 storage header에만 존재한다.

---

## 6. Storage Header Block

storage header는 block 0에 위치한다.

### 6.1 Layout

```text
Storage Header Block
+------------------------+--------------------+-----------------+
| StorageHeader (28B)    | Reserved / Padding | block_crc32 (4B)|
+------------------------+--------------------+-----------------+
                                                offset block_size-4
```

### 6.2 StorageHeader 구조체

Endian: little-endian
Size: 28 bytes (block CRC32는 별도로 `block_size - 4`에 저장)

| Offset | Size | Type  | Field         | 설명                         |
| ------ | ---- | ----- | ------------- | -------------------------- |
| 0      | 1    | u8    | block_type    | `0x01`                     |
| 1      | 3    | u8[3] | magic         | ASCII `"EKV"`              |
| 4      | 2    | u16   | version_major | major format version       |
| 6      | 2    | u16   | version_minor | minor format version       |
| 8      | 4    | u32   | block_size    | block size in bytes        |
| 12     | 4    | u32   | block_count   | storage 내 전체 block 수       |
| 16     | 4    | u32   | replica_id    | storage replica 식별자 (replica device마다 부여) |
| 20     | 4    | u32   | format_seq    | storage format 세대          |
| 24     | 4    | u32   | flags         | storage-level flags        |

`block_crc32`는 block 0의 마지막 4바이트(`block_size - 4`)에 저장하며, block 0 전체에 대해 계산한다. 계산 시 `block_size - 4` 위치의 4바이트는 `0x00000000`으로 간주한다.

### 6.3 Storage Header 검증 조건

storage header는 다음 조건을 모두 만족해야 유효하다.

* `block_type == 0x01`
* `magic == "EKV"`
* 지원 가능한 version
* `block_size`가 구현체가 지원하는 값
* `block_count >= 1`
* storage 실제 크기와 `block_size * block_count`가 일치
* 마지막 4바이트의 block CRC32 검증 성공

---

## 7. Record Descriptor Block

record descriptor는 하나의 key-value record를 대표하는 블록이다. 하나의 key에 대한 value가 여러 블록에 나뉘어 저장되더라도, record의 기준점은 record descriptor이다.

용량 절약을 위해 record descriptor block에는 첫 번째 value chunk를 함께 저장한다.

```text
Record Descriptor Block
+--------------------------+----------------------+
| RecordDescriptorHeader   | First Value Payload  |
+--------------------------+----------------------+
```

### 7.1 RecordDescriptorHeader 구조체

Endian: little-endian
Size: 32 bytes (block CRC32는 별도로 `block_size - 4`에 저장)

| Offset | Size | Type | Field              | 설명                                           |
| ------ | ---- | ---- | ------------------ | -------------------------------------------- |
| 0      | 1    | u8   | block_type         | `0x02`                                       |
| 1      | 1    | u8   | header_size        | `32`                                         |
| 2      | 2    | u16  | key_size           | key 바이트 수 (UTF-8 인코딩)                        |
| 4      | 4    | u32  | generation         | 동일 key에 대한 갱신 순서                             |
| 8      | 4    | u32  | total_size         | 전체 value 크기                                  |
| 12     | 4    | u32  | first_payload_size | descriptor block 내 value payload 크기          |
| 16     | 4    | u32  | chunk_count        | descriptor 포함 전체 value chunk 수               |
| 20     | 4    | u32  | next_chunk         | 첫 번째 value chunk block index 또는 `0xFFFFFFFF` |
| 24     | 4    | u32  | flags              | record 내부 flags (예약, 0으로 설정)                 |
| 28     | 4    | u32  | user_flags         | 사용자 정의 flags. 포맷이 그대로 보존하며 의미는 caller 정의      |

`block_crc32`는 block의 마지막 4바이트(`block_size - 4`)에 저장한다.

### 7.2 Key 및 Payload 위치

descriptor block의 레이아웃은 다음과 같다.

```text
Record Descriptor Block
+------------------------------+------------------+---------------------+---------+-----------------+
| RecordDescriptorHeader (32B) | Key (key_size B) | First Value Payload | Padding | block_crc32 (4B)|
+------------------------------+------------------+---------------------+---------+-----------------+
                                                                                   offset block_size-4
```

key는 고정 header 바로 다음 offset에 저장된다. value payload는 key 다음에 위치한다.

```text
key_offset             = 32
first_payload_offset   = 32 + key_size
first_payload_capacity = block_size - 32 - key_size - 4   (마지막 4바이트는 CRC 용으로 예약)
```

`key_size`는 `block_size - 32 - 4`보다 작거나 같아야 한다. `first_payload_size`는 `first_payload_capacity`보다 작거나 같아야 한다.

value 전체가 descriptor block 안에 들어가면 다음 조건을 만족한다.

```text
total_size == first_payload_size
chunk_count == 1
next_chunk == 0xFFFFFFFF
```

value가 추가 chunk를 필요로 하면 다음 조건을 만족한다.

```text
total_size > first_payload_size
chunk_count > 1
next_chunk != 0xFFFFFFFF
```

### 7.3 Key Format

key는 UTF-8 인코딩된 문자열이다. key 바이트는 offset `36`부터 `key_size` 바이트 연속으로 저장된다.

key 동일성 판단은 저장된 key 바이트를 직접 비교하여 수행한다.

### 7.4 Descriptor Block CRC

descriptor block의 `block_crc32`는 descriptor header, key bytes, first value payload를 포함한 block 전체에 대해 계산한다.

계산 시 `block_size - 4` 위치의 4바이트는 `0x00000000`으로 간주한다.

descriptor block의 남는 영역(padding)은 deterministic한 값으로 채워야 한다. 권장값은 `0x00`이다.

---

## 8. Value Chunk Block

value chunk는 record descriptor에 담기지 못한 나머지 value payload를 저장한다.

```text
Value Chunk Block
+--------------------+---------+
| ValueChunkHeader   | Payload |
+--------------------+---------+
```

### 8.1 ValueChunkHeader 구조체

Endian: little-endian
Size: 20 bytes (block CRC32는 별도로 `block_size - 4`에 저장)

| Offset | Size | Type | Field            | 설명                                         |
| ------ | ---- | ---- | ---------------- | ------------------------------------------ |
| 0      | 1    | u8   | block_type       | `0x03`                                     |
| 1      | 1    | u8   | header_size      | `20`                                       |
| 2      | 2    | u16  | flags            | chunk flags                                |
| 4      | 4    | u32  | owner_descriptor | 이 chunk가 속한 descriptor block index         |
| 8      | 4    | u32  | chunk_index      | descriptor payload를 0으로 하는 chunk index     |
| 12     | 4    | u32  | payload_size     | 현재 chunk payload 크기                        |
| 16     | 4    | u32  | next_chunk       | 다음 value chunk block index 또는 `0xFFFFFFFF` |

`block_crc32`는 block의 마지막 4바이트(`block_size - 4`)에 저장한다.

### 8.2 Payload 위치

value chunk payload는 offset 20부터 시작한다.

```text
payload_offset = 20
payload_capacity = block_size - 20 - 4   (마지막 4바이트는 CRC 용으로 예약)
```

`payload_size`는 `payload_capacity`보다 작거나 같아야 한다.

### 8.3 Chunk Index 규칙

record descriptor에 포함된 first payload는 논리적으로 `chunk_index == 0`이다.

별도 value chunk block은 `chunk_index >= 1`이어야 한다.

예를 들어 value가 descriptor payload와 두 개의 value chunk로 구성되면 다음과 같다.

```text
Record Descriptor: chunk_index 0
Value Chunk A:     chunk_index 1
Value Chunk B:     chunk_index 2
```

이때 descriptor의 `chunk_count`는 `3`이다.

### 8.4 Chunk Block CRC

value chunk block의 `block_crc32`는 chunk header와 payload를 포함한 block 전체에 대해 계산한다.

계산 시 `block_size - 4` 위치의 4바이트는 `0x00000000`으로 간주한다.

chunk block의 남는 영역은 deterministic한 값으로 채워야 한다. 권장값은 `0x00`이다.

---

## 9. Free Block

free block은 write 가능한 빈 블록이다.

free block은 두 가지 형태를 모두 허용한다.

| 형태          | 조건                   | 용도                    |
| ----------- | -------------------- | --------------------- |
| zero free   | block 첫 byte가 `0x00` | zero-filled format 대응 |
| erased free | block 첫 byte가 `0xFF` | erased NAND 대응        |

free block에는 구조체 header, next pointer, CRC가 없다.

```text
Free Block
+------------------------------+
| 0x00... or 0xFF...           |
+------------------------------+
```

block의 첫 byte가 `0x00` 또는 `0xFF`이면 free block 후보로 볼 수 있다. 단, 구현체는 보수적으로 동작해야 하며, valid block CRC를 가진 non-free block을 free로 오인하면 안 된다.

권장 판정 순서는 다음과 같다.

```text
1. block_type이 0x01, 0x02, 0x03인지 확인한다.
2. 해당 type의 block CRC를 검증한다.
3. CRC가 유효하면 non-free block으로 처리한다.
4. 그렇지 않고 첫 byte가 0x00 또는 0xFF이면 free block으로 처리한다.
5. 나머지는 garbage로 처리한다.
```

---

## 10. Record 구성

하나의 record는 하나의 record descriptor와 0개 이상의 value chunk로 구성된다.

```text
Record
+-------------------------------+---------------+---------------+
| Record Descriptor + Chunk 0   | Chunk 1       | Chunk 2       |
+-------------------------------+---------------+---------------+
```

작은 value는 descriptor block 하나만으로 구성된다.

```text
Small Record
+-------------------------------+
| Record Descriptor + Full Value |
+-------------------------------+
```

큰 value는 descriptor block과 value chunk block들로 구성된다.

```text
Large Record
+-------------------------------+---------------+---------------+
| Record Descriptor + Chunk 0   | Value Chunk 1 | Value Chunk 2 |
+-------------------------------+---------------+---------------+
```

---

## 11. Free Space 관리

embedkv는 영속 free list를 유지하지 않는다.

free block 목록은 다음 방식으로 처리한다.

* recovery 시 storage 전체를 scan한다.
* valid record에 속하지 않는 block을 garbage로 판단한다.
* garbage block은 free block으로 제거한다.
* write 시점에는 storage를 scan하여 그때그때 필요한 free block을 찾는다.

따라서 free block에는 `next_free` 같은 연결 정보가 없다.

### 11.1 Write 시 Free Block 탐색

write 시 필요한 block 수를 계산한 뒤 storage를 순차 scan하여 free block을 찾는다.

```text
1. 필요한 block 수를 계산한다.
2. block 1부터 순차 scan한다.
3. 첫 byte가 0x00 또는 0xFF인 block을 free 후보로 본다.
4. 필요한 개수만큼 free block을 확보한다.
5. 확보한 block들에 descriptor와 value chunk를 기록한다.
```

이 방식은 free list를 유지하기 위한 추가 write를 제거한다.

---

## 12. Write 구조

embedkv는 순차적 write에 적합하도록 설계한다. write 대상 block들은 가능한 한 낮은 block index부터 높은 block index 방향으로 선택한다.

새 record는 기존 record를 덮어쓰지 않고 free block에 기록한다.

### 12.1 신규 Write 절차

신규 key write는 다음 순서로 수행한다.

```text
1. key와 value를 입력받는다.
2. value를 descriptor payload와 value chunk payload들로 나눈다.
3. 필요한 free block 수를 계산한다.
4. storage를 scan하여 필요한 free block들을 찾는다.
5. record descriptor block을 쓴다.
6. value chunk block들을 쓴다.
7. Flush 한다.
```

descriptor를 반드시 마지막에 써야 한다는 요구사항은 없다. 구현체는 순차 write에 유리한 순서로 descriptor와 value chunk를 배치하고 기록할 수 있다.

record는 다음 조건을 만족할 때만 완전한 것으로 인정된다.

* descriptor block CRC가 유효하다.
* descriptor가 참조하는 모든 value chunk block CRC가 유효하다.
* chunk chain이 끊기지 않는다.
* chunk index와 chunk count가 일치한다.
* payload size 합계가 total size와 일치한다.

별도의 value 전체 CRC는 사용하지 않는다. 각 block의 CRC가 해당 block의 payload와 header를 보호한다.

---

## 13. Update 절차

update는 copy-on-write 방식으로 수행한다. 새 record를 먼저 free block에 쓴 뒤 flush하고, 그 다음 이전 record block들을 제거한 뒤 다시 flush한다.

```text
1. 기존 key의 최신 완전 record를 찾는다.
2. 새 value를 descriptor payload와 value chunk payload들로 나눈다.
3. 필요한 free block 수를 계산한다.
4. storage를 scan하여 필요한 free block들을 찾는다.
5. 새 record descriptor block을 쓴다.
6. 새 value chunk block들을 쓴다.
7. Flush 한다.
8. 이전 record의 descriptor block과 value chunk block들을 free block으로 제거한다.
9. Flush 한다.
```

중요한 점은 이전 record 제거가 첫 번째 flush 이후에 수행된다는 것이다. 첫 번째 flush가 완료되기 전에는 이전 record가 남아 있어야 한다.

이 구조에서 update 중 전원 차단이 발생하면 recovery는 다음 원칙으로 동작한다.

* 새 record가 완전하면 새 generation을 사용한다.
* 새 record가 불완전하면 이전 generation을 사용한다.
* 이전 record 제거 중 전원 차단이 발생하면 남아 있는 완전한 최신 generation을 사용한다.
* 불완전하거나 orphan 상태가 된 block은 recovery 시 garbage로 제거한다.

---

## 14. Delete 절차

key 삭제는 해당 key의 최신 record block들을 free block으로 제거하는 방식으로 처리한다.

```text
1. key의 최신 완전 record를 찾는다.
2. record descriptor와 value chunk block들을 free block으로 제거한다.
3. Flush 한다.
```

삭제 도중 전원 차단이 발생하면 일부 block만 제거될 수 있다. 이 경우 recovery는 descriptor와 chunk chain 검증을 통해 완전하지 않은 record를 garbage로 제거한다.

---

## 15. Flush 규칙

Flush는 storage 장치에 이전 write들이 영속화되었음을 보장하기 위한 barrier이다.

embedkv는 다음 지점에서 flush를 요구한다.

### 15.1 신규 Write

```text
1. descriptor 쓰기
2. value chunk 쓰기
3. Flush
```

flush 완료 후 해당 record는 read 가능한 후보가 된다.

### 15.2 Update

```text
1. 새 descriptor 쓰기
2. 새 value chunk 쓰기
3. Flush
4. 이전 descriptor 제거
5. 이전 value chunk 제거
6. Flush
```

첫 번째 flush는 새 record의 영속화를 보장한다. 두 번째 flush는 이전 record 제거의 영속화를 보장한다.

### 15.3 Recovery 이후 Garbage 제거

```text
1. garbage block 제거
2. Flush
```

recovery가 garbage를 free block으로 제거했다면 flush를 수행해야 한다.

### 15.4 Flush 실패

flush 실패 시 해당 write 또는 update는 완료된 것으로 간주하지 않는다.

---

## 16. Read 절차

read는 key에 해당하는 record descriptor들을 찾고, 그중 완전한 최신 record를 선택한다.

```text
1. storage를 scan하거나 메모리 index에서 key가 일치하는 descriptor 후보를 찾는다.
2. descriptor block CRC를 검증한다.
3. 저장된 key 바이트를 읽고 요청한 key와 직접 비교한다.
4. key가 일치하는 descriptor 중 generation이 높은 순서대로 record 후보를 검사한다.
5. descriptor의 first payload를 읽는다.
6. next_chunk를 따라 value chunk들을 읽는다.
7. 각 value chunk block CRC를 검증한다.
8. owner_descriptor, chunk_index, chunk_count를 검증한다.
9. payload_size 합계와 total_size가 일치하는지 확인한다.
10. 가장 높은 generation의 완전한 record를 반환한다.
```

높은 generation의 record가 존재하더라도 불완전하면 읽지 않는다. 그 경우 낮은 generation 중 완전한 record를 사용할 수 있다.

---

## 17. Recovery

복구는 storage 전체를 scan하여 수행한다. recovery는 free list를 재구성하지 않는다. 대신 garbage를 제거하고, 이후 write 시점에 free block을 다시 scan한다.

```text
1. storage header를 읽고 검증한다.
2. 모든 block을 순차적으로 scan한다.
3. block CRC가 유효한 record descriptor들을 수집한다.
4. 각 descriptor의 key 바이트를 읽는다.
5. 각 descriptor에서 next_chunk chain을 따라 value chunk들을 읽는다.
6. 각 value chunk block CRC를 검증한다.
7. chunk_count, chunk_index, owner_descriptor, total_size를 검증한다.
8. 실제 key 바이트 기준으로 key별 가장 높은 generation의 완전한 record를 선택한다.
9. 선택된 최신 record에 속하지 않는 block을 garbage로 분류한다.
10. 불완전 record에 속한 block을 garbage로 분류한다.
11. descriptor 없이 남은 value chunk block을 garbage로 분류한다.
12. garbage block을 free block으로 제거한다.
13. Flush 한다.
```

복구 과정은 보수적으로 동작한다. 애매한 block은 read 대상으로 사용하지 않고 garbage로 분류한다.

---

## 18. Garbage 제거

garbage 제거는 대상 block을 free block 형태로 write하는 것이다.

free block은 `0x00` 또는 `0xFF` 형식을 사용할 수 있다.

### 18.1 Zero Free 방식

```text
block[0..block_size-1] = 0x00
```

### 18.2 Erased Free 방식

```text
block[0..block_size-1] = 0xFF
```

사용할 free pattern은 storage backend 특성에 따라 선택한다.

| Backend 특성                     | 권장 free pattern |
| ------------------------------ | --------------- |
| zero-filled file 또는 HDD format | `0x00`          |
| erased NAND flash              | `0xFF`          |
| 추상 block device                | 구현체 선택          |

garbage 제거 후에는 flush가 필요하다.

---

## 19. 불완전 Record 판정 기준

다음 조건 중 하나라도 만족하면 record는 불완전한 것으로 본다.

* descriptor block CRC 불일치
* descriptor의 `block_type != 0x02`
* descriptor의 `header_size != 32`
* `key_size > block_size - 32 - 4` (key가 block에 들어가지 않음)
* `first_payload_size > block_size - 32 - key_size - 4`
* `chunk_count == 0`
* `total_size`와 payload size 합계 불일치
* `chunk_count`와 실제 chunk 개수 불일치
* `next_chunk`가 storage 범위를 벗어남
* chunk chain이 중간에 끊김
* chunk chain에 cycle 존재
* value chunk block CRC 불일치
* value chunk의 `block_type != 0x03`
* value chunk의 `header_size != 20`
* value chunk의 `owner_descriptor` 불일치
* value chunk의 `chunk_index` 불일치
* value chunk의 `payload_size > block_size - 20 - 4`

불완전 record는 read 결과로 반환하지 않는다.

---

## 20. Replica 구조

embedkv의 replica는 block 단위 복제가 아니라 storage 전체 복제본을 의미한다.

```text
Replica Set
+-------------------+
| Storage Replica 0 |
+-------------------+
| Storage Replica 1 |
+-------------------+
| Storage Replica 2 |
+-------------------+
```

각 replica는 독립적인 embedkv storage이다. 동일한 key-value 데이터가 여러 storage replica에 기록될 수 있으며, 복구 시 replica 단위로 완전성을 비교한다.

### 20.0 Store와 Replica Device

store는 하나 이상의 replica BlockDevice 위에서 동작한다. `Format`과 `Open`은 모두 **device 목록**을 받는다. 단일 device 사용은 1개짜리 목록을 넘기는 특수한 경우일 뿐이다.

* `Format(devices, ...)`은 각 device의 block 0에 storage header를 기록하며, replica `i`의 `replica_id`는 `base + i`로 부여한다.
* 모든 replica는 동일한 `block_size`와 `block_count`를 가져야 한다. geometry가 다르면 `Open`이 거부한다.
* write는 모든 replica에 fan-out되며, 각 replica는 독립적으로 flush한다. 동일 generation(`replica 전체의 최대 generation + 1`)을 모든 replica에 기록하여 정렬을 유지한다.
* read는 replica들 중 가장 높은 generation의 완전한 record를 반환한다. 어떤 replica의 record가 손상되었거나 없으면 다음 replica로 fallback한다.

### 20.1 Replica Write

replica가 여러 개 있는 경우 write는 각 storage replica에 독립적으로 수행된다.

```text
1. Replica 0에 record write
2. Replica 0 Flush
3. Replica 1에 record write
4. Replica 1 Flush
5. Replica 2에 record write
6. Replica 2 Flush
```

일부 replica에서 write 또는 flush가 실패하더라도, 다른 replica에 완전한 record가 남아 있으면 복구 가능하다.

### 20.2 Replica Update

각 replica에서 update는 동일한 순서를 따른다.

```text
1. 새 descriptor 쓰기
2. 새 value chunk 쓰기
3. Flush
4. 이전 descriptor 제거
5. 이전 value chunk 제거
6. Flush
```

recovery는 각 replica를 독립적으로 검증한다.

### 20.3 Replica Recovery

복구 시에는 각 storage replica를 독립적으로 scan한다.

```text
1. 각 replica의 storage header를 검증한다.
2. 각 replica에서 key별 최신 완전 record를 찾는다.
3. replica들 사이에서 가장 최신 generation의 완전 record를 선택한다.
4. 선택된 record를 기준으로 손상되거나 오래된 replica를 복구할 수 있다.
```

동일 key에 대해 replica마다 다른 generation이 있을 수 있다. 이 경우 가장 높은 generation 중 완전한 record를 우선한다.

```text
Replica 0: Key A generation 5 complete
Replica 1: Key A generation 6 incomplete
Replica 2: Key A generation 4 complete

Selected: Replica 0 generation 5
```

generation 6이 존재하더라도 완전하지 않으므로 선택하지 않는다.

---

## 21. 메모리 Index

embedkv는 성능을 위해 메모리 상에 key index를 유지할 수 있다.

```text
key (UTF-8 string) -> latest complete record descriptor
```

인덱스의 map key는 실제 key 바이트 전체이다. key 동일성은 byte 수준 비교로 판단한다.

index 항목은 다음 정보를 가진다.

| 필드               | 설명                        |
| ---------------- | ------------------------- |
| key              | 실제 key 바이트 (UTF-8 string) |
| generation       | 선택된 generation            |
| descriptor_block | record descriptor 위치      |
| total_size       | 전체 value 크기               |

이 index는 영속 상태의 필수 구성요소가 아니다. 재시작 시 storage scan을 통해 다시 만들 수 있어야 한다.

---

## 22. 정합성 규칙

embedkv의 정합성 규칙은 다음과 같다.

1. storage header는 block 0에만 존재한다.
2. 개별 data block에는 magic/version을 반복 저장하지 않는다.
3. free block은 `0x00` 또는 `0xFF` 형태를 허용한다.
4. free block을 제외한 모든 block은 block CRC32를 가진다.
5. block CRC가 실패한 block은 read하지 않는다.
6. record descriptor는 record의 기준점이다.
7. descriptor와 연결된 모든 value chunk가 검증되어야 record가 완전하다.
8. 별도의 value 전체 CRC는 사용하지 않는다.
9. 동일 key(실제 key 바이트 기준)에 여러 record가 있으면 가장 높은 generation의 완전한 record를 선택한다.
10. 높은 generation이 불완전하면 낮은 generation의 완전한 record를 사용할 수 있다.
11. update는 새 record flush 이후 기존 record를 제거한다.
12. 기존 record 제거 후 다시 flush한다.
13. recovery 시 garbage를 제거하고 flush한다.
14. write 시 free block은 그때그때 scan하여 찾는다.
15. replica는 storage 단위로 판단한다.
16. 여러 replica 중 하나라도 완전한 최신 record를 가지고 있으면 복구 가능하다.

---

## 23. 예시

### 23.1 작은 Value

```text
Block 0: Storage Header
Block 1: Record Descriptor + Full Value
Block 2: Free Block
Block 3: Free Block
```

record descriptor 안에 value 전체가 들어가므로 추가 value chunk가 필요 없다.

### 23.2 큰 Value

```text
Block 0: Storage Header
Block 1: Record Descriptor + Value Part 0
Block 2: Value Chunk 1
Block 3: Value Chunk 2
Block 4: Free Block
```

record descriptor는 전체 value의 첫 부분을 포함하고, 나머지는 value chunk들이 저장한다.

### 23.3 Update 완료 후 상태

update 직후 첫 번째 flush가 완료되면 새 record와 이전 record가 동시에 존재할 수 있다.

```text
Block 0: Storage Header
Block 1: Key A generation 1 descriptor
Block 2: Key A generation 1 chunk
Block 3: Key A generation 2 descriptor
Block 4: Key A generation 2 chunk
```

이후 이전 record 제거와 두 번째 flush가 완료되면 다음과 같이 된다.

```text
Block 0: Storage Header
Block 1: Free Block
Block 2: Free Block
Block 3: Key A generation 2 descriptor
Block 4: Key A generation 2 chunk
```

### 23.4 Update 중 전원 차단

```text
Block 0: Storage Header
Block 1: Key A generation 1 descriptor
Block 2: Key A generation 1 chunk
Block 3: Key A generation 2 descriptor
Block 4: partially written chunk
```

generation 2 record가 불완전하면 read는 generation 1을 반환한다. recovery는 generation 2의 불완전 block을 garbage로 제거한다.

### 23.5 이전 Record 제거 중 전원 차단

```text
Block 0: Storage Header
Block 1: Free Block
Block 2: Key A generation 1 chunk
Block 3: Key A generation 2 descriptor
Block 4: Key A generation 2 chunk
```

generation 2가 완전하면 read는 generation 2를 반환한다. generation 1의 남은 chunk는 descriptor가 없거나 최신 record에 속하지 않으므로 recovery에서 garbage로 제거된다.

---

## 24. 요약

embedkv는 고정 크기 스토리지 위에서 동작하는 작은 key-value 스토리지이다. 전체 storage는 고정 크기 블록들로 구성되며, 첫 번째 블록은 storage header로 사용된다.

개별 data block에는 magic/version 같은 반복 메타데이터를 두지 않고, block type과 참조에 필요한 최소한의 header만 둔다. free block은 `0x00` 또는 `0xFF` 형태를 모두 허용한다.

record descriptor는 하나의 record를 대표하며, 용량 절약을 위해 첫 번째 value chunk를 함께 포함한다. 나머지 value는 value chunk block에 저장된다.

free list는 유지하지 않는다. recovery 시 garbage를 제거하고, write 시 필요한 free block을 storage scan으로 그때그때 찾는다.

free block을 제외한 모든 block은 block CRC32를 가진다. record 완전성은 descriptor와 value chunk 각각의 block CRC, chunk chain, payload size, chunk count 검증으로 판단한다. 별도의 value 전체 CRC는 사용하지 않는다.

update는 새 descriptor와 value chunk를 쓴 뒤 flush하고, 이후 기존 descriptor와 value chunk들을 free block으로 제거한 뒤 다시 flush한다. 이 구조를 통해 전원 차단 상황에서도 이전 record 또는 새 record 중 완전한 record를 선택할 수 있다.

replica는 block 단위가 아니라 storage 전체 복제본이다. 여러 storage replica 중 완전한 최신 record를 가진 replica를 선택하여 읽거나 복구할 수 있다.

이 구조를 통해 embedkv는 제한된 저장 공간에서도 낮은 write 횟수, 명확한 flush 지점, block 단위 무결성 검증, 전원 차단 안전성, storage replica 기반 복구를 제공한다.
