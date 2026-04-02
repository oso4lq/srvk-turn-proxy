# Stealth Mode

Stealth-слой маскирует VPN-трафик под WebRTC видеозвонок. Трафик между клиентом и сервером оборачивается в stealth-протокол с padding, pacing и dummy-пакетами, что затрудняет обнаружение через DPI.

## Использование

```bash
# Сервер
./server -connect wg-server:51820 -stealth

# Клиент
./client -vk-link "https://vk.com/call/join/..." -peer server:56000 -stealth

# С выбором режима pacing
./client ... -stealth -stealth-pacing mixed

# Env-переменные (fallback, если флаги не заданы)
STEALTH_MODE=true
STEALTH_PACING_MODE=video
STEALTH_BUF_SIZE=64
```

### CLI-флаги

| Флаг | Описание | По умолчанию |
|------|----------|-------------|
| `-stealth` | Включить stealth-слой | `false` |
| `-stealth-pacing` | Режим pacing: `audio`, `video`, `mixed` | `video` |
| `-stealth-buf` | Размер буфера пакетов | `64` |
| `-stealth-min` | Минимум каналов в stealth mode (клиент) | `2` |

Флаги доступны и в клиенте, и в сервере (кроме `-stealth-min` — только клиент).

## Обратная совместимость

Сервер с `-stealth` автоматически определяет тип клиента по первому байту входящего пакета:

- **0xA1–0xA3** → stealth-клиент → запускается Pipeline с pacing
- **0x01–0x04** → legacy WireGuard-клиент → прямой relay без изменений

Старые клиенты продолжают работать без обновления. Клиент без `-stealth` работает как раньше.

## Архитектура

Stealth-слой встраивается между DTLS-соединением и WireGuard, не затрагивая существующий DTLS:

```
WireGuard app
      ↓
[STEALTH: padding + pacing + dummy]   ← новый слой
      ↓
[DTLS 1.2 — существующая обфускация]
      ↓
[TURN / VK инфраструктура]
```

### Пакет `stealth/`

```
stealth/
├── framer.go          # Wire-формат: encode/decode, флаги, version info
├── framer_test.go
├── padder.go          # Выбор target size с профилями audio/video
├── padder_test.go
├── pacer.go           # Pacing-таймер, dummy-генерация, bidirectional relay, Metrics()
├── pacer_test.go
├── adaptive.go        # Advisor — калькулятор масштабирования каналов
├── adaptive_test.go
├── pipeline.go        # Композиция компонентов, Config, version negotiation, Metrics()
└── pipeline_test.go
```

### Компоненты

**Framer** — кодирует и декодирует stealth wire-формат. Чистые функции без состояния.

**Padder** — выбирает целевой размер пакета на основе профилей audio/video. Stateless, потокобезопасный.

**Pacer** — управляет ритмичной отправкой пакетов по таймеру с буферизацией. Генерирует dummy-пакеты в пустых слотах. Поддерживает три режима pacing.

**Pipeline** — компонует Framer, Padder и Pacer в единый bidirectional relay. Обрабатывает version negotiation при подключении и legacy fallback. Экспортирует `Metrics()` для Advisor.

**Advisor** (`adaptive.go`) — чистый калькулятор масштабирования каналов. Pull-модель: клиент вызывает `Tick()` каждые 5 секунд с метриками всех активных Pacer'ов, получает `ScaleAdvice` (Hold / ScaleUp / ScaleDown). Без горутин и I/O — тестируется тривиально.

## Wire-формат

Каждый stealth-пакет:

```
[1 byte: flags] [2 bytes: payload_len BE] [payload] [random padding]
```

| Флаг | Значение | Описание |
|------|----------|----------|
| `0xA1` | FlagData | Реальные данные |
| `0xA2` | FlagDummy | Dummy-пакет (payload_len = 0) |
| `0xA3` | FlagKeepalive | Keepalive (зарезервировано) |

Значения 0xA1–0xA3 не пересекаются с WireGuard message types (0x01–0x04), что позволяет серверу различать stealth и legacy клиентов по первому байту.

### Version info

Первый пакет от клиента содержит 4 байта version info в начале payload:

```
[version: 1 byte] [pacing_mode: 1 byte] [reserved: 2 bytes]
```

Сервер извлекает version info, логирует параметры клиента и передаёт оставшиеся данные в WireGuard.

## Padding

Каждый пакет дополняется случайным padding до размера, характерного для WebRTC:

| Профиль | Payload | Target size | Имитирует |
|---------|---------|-------------|-----------|
| Audio | ≤ 197 байт | 100–200 байт | Opus аудио-фреймы |
| Video | > 197 байт | 800–1400 байт | VP8/VP9/H.264 видео-фреймы |

- Target size выбирается случайно в диапазоне профиля с jitter ±5 байт
- Padding заполняется криптографически случайными байтами
- Если payload + header > максимума профиля — переход в следующий профиль
- Oversize пакеты (> 1400 байт) отправляются без padding

## Pacing

Пакеты отправляются по таймеру, а не по факту появления данных:

| Режим | Интервал | Описание |
|-------|----------|----------|
| `audio` | ~20 мс | Один таймер, имитация аудио-потока |
| `video` | ~33 мс | Один таймер, имитация видео-потока |
| `mixed` | ~20 мс + ~33 мс | Два параллельных таймера (аудио + видео), как в реальном звонке |

- Каждый интервал имеет jitter ±2 мс
- Если данные есть — отправляется data-пакет с padding
- Если данных нет — отправляется dummy-пакет
- Dummy-пакеты отбрасываются принимающей стороной

В режиме `mixed` маленькие пакеты (≤ 200 байт) идут через аудио-таймер, большие — через видео-таймер. Оба потока мультиплексируются в один канал записи.

## Adaptive Channels

В stealth-mode количество параллельных каналов масштабируется автоматически:

| Параметр | Описание | По умолчанию |
|----------|----------|-------------|
| `-stealth-min` | Минимум каналов | `2` |
| `-n` | Максимум каналов (потолок) | `16` |

- При старте создаётся `stealth-min` каналов
- Каждые 5 секунд проверяется утилизация буфера Pacer
- Утилизация > 70% стабильно 15с → добавляется канал
- Утилизация < 20% стабильно 15с → убирается последний канал
- Упавшие каналы автоматически восстанавливаются до `stealth-min`

В legacy-режиме `-n` — фиксированное число каналов (поведение не изменилось).

### Архитектура Adaptive Channels

```
stealth/adaptive.go              client/main.go
─────────────────                ─────────────
Advisor:                         channelManager:
  знает: утилизация буфера    →    знает: как создать канал
  знает: пороги 70%/20%            знает: как удалить канал
  знает: время стабилизации         не знает: откуда advice
  решает: scale up/down/hold        реагирует: на advice
```

- **Pacer.Metrics()** — экспортирует `{BufLen, BufCap}` (заполнение/ёмкость outCh). Потокобезопасен без мьютекса (`len()`/`cap()` на каналах безопасны). В mixed mode суммирует audioCh + videoCh
- **Pipeline.Metrics()** — проксирует вызов к внутреннему Pacer
- **Advisor.Tick([]PacerMetrics)** — агрегирует утилизацию всех каналов, пропускает `BufCap==0` (канал ещё не запущен). Решение принимается после `StableCount` (3) тиков подряд выше/ниже порога
- **channelManager** — управляет набором `activeChannel` (cancel + pipeline + done). `add()` неблокирующий — handshake в отдельной горутине. `removeLast()` — LIFO, отменяет последний (самый свежий) канал
- **adaptiveLoop** — каждые ~30с логирует статус: число каналов, утилизация буфера (%), приблизительная макс. пропускная способность (Мбит/с). Оценка скорости — по формуле `каналы × pps × maxPayload`, без byte counters
- **adaptiveLoop** — горутина, тикает каждые 5с: cleanup завершённых → hard floor до min → Tick → scale up/down

## DTLS Fingerprint Guard

CI-тесты фиксируют эталонную DTLS-конфигурацию сервера и клиента:

- `server/dtls_fingerprint_test.go` — CipherSuites, ExtendedMasterSecret, CID generator
- `client/dtls_fingerprint_test.go` — то же + InsecureSkipVerify

Если merge с upstream или рефакторинг изменит DTLS-параметры, тесты упадут.

## Dummy traffic

Туннель никогда не молчит, пока соединение активно:

- В каждом pacing-слоте без реальных данных отправляется dummy-пакет (flags = 0xA2, payload_len = 0)
- Dummy-пакет дополняется random padding до размера аудио-профиля
- Принимающая сторона распознаёт dummy по флагу и отбрасывает
- Со стороны DPI поток выглядит как непрерывный аудио/видео-поток

## Модификации существующего кода

### `server/main.go`

- Добавлены CLI-флаги `-stealth`, `-stealth-pacing`, `-stealth-buf`
- При `-stealth`: первый пакет читается для определения stealth/legacy клиента
- Stealth-клиент → `Pipeline.RunWithFirstPacket()` с pacing
- Legacy-клиент → прямой relay как раньше
- DTLS-конфигурация извлечена в `serverDTLSConfig()` — фиксируется fingerprint-тестом

### `client/main.go`

- Добавлены CLI-флаги `-stealth`, `-stealth-pacing`, `-stealth-buf`, `-stealth-min`
- DTLS-конфигурация извлечена в `clientDTLSConfig()` — фиксируется fingerprint-тестом
- `packetConnAdapter` — адаптер `net.PacketConn` → `net.Conn` для совместимости с Pipeline
- `oneDtlsConnection` принимает внешний `*stealth.Pipeline` (для сбора метрик из channelManager)
- При `-stealth`: три пути выполнения:
  - **direct** (`-no-dtls`): только TURN, без DTLS
  - **stealth**: `channelManager` + `adaptiveLoop` — динамические каналы
  - **legacy**: фиксированные каналы через `oneDtlsConnectionLoop` / `oneTurnConnectionLoop`
- TURN context исправлен: `context.Background()` → канал-специфичный `ctx` для корректной остановки при ScaleDown

### Edge cases

| Ситуация | Поведение |
|----------|-----------|
| Pacer.Metrics() до run() | Возвращает `{0, 0}`, Advisor пропускает BufCap==0 → Hold |
| Канал умирает (DTLS/TURN ошибка) | cleanup() на следующем тике удаляет, hard floor восстанавливает до min |
| ScaleDown с данными в буфере | cancel() → Pacer drain writeCh → UDP потери допустимы |
| Все каналы на максимуме | ScaleUp игнорируется, back-pressure через outCh → WireGuard дропает |
| Handshake при ScaleUp (~1-5с) | add() неблокирующий, стартующий канал: Metrics = {0, 0} → пропускается |
| `-stealth-min` > `-n` | min принудительно уменьшается до max |
