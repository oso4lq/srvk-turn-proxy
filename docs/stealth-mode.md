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

Флаги доступны и в клиенте, и в сервере.

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
├── pacer.go           # Pacing-таймер, dummy-генерация, bidirectional relay
├── pacer_test.go
├── pipeline.go        # Композиция компонентов, Config, version negotiation, legacy fallback
└── pipeline_test.go
```

### Компоненты

**Framer** — кодирует и декодирует stealth wire-формат. Чистые функции без состояния.

**Padder** — выбирает целевой размер пакета на основе профилей audio/video. Stateless, потокобезопасный.

**Pacer** — управляет ритмичной отправкой пакетов по таймеру с буферизацией. Генерирует dummy-пакеты в пустых слотах. Поддерживает три режима pacing.

**Pipeline** — компонует Framer, Padder и Pacer в единый bidirectional relay. Обрабатывает version negotiation при подключении и legacy fallback.

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

### `client/main.go`

- Добавлены те же CLI-флаги
- Добавлен `packetConnAdapter` — адаптер `net.PacketConn` → `net.Conn` для совместимости с Pipeline
- При `-stealth`: в `oneDtlsConnection` вместо прямого relay запускается `Pipeline.RunAsClient()`
- Первый пакет от клиента автоматически обогащается version info
