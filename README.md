# Тестовое задание CDNNow — решение на Go

Переписанные на Go `calculator_server` и `generator` (+ роут `/metrics` в формате Prometheus).

## Структура

```
go.mod               — модуль cdnnow/calc (stdlib + cgo, без внешних зависимостей)
server/main.go       — HTTP-сервер, флаги, graceful shutdown, периодический вывод
server/query.go      — разбор ?num= без аллокаций на запрос
server/native.go     — вызов C/Rust через dlopen (как ctypes), fallback на Go
server/metrics.go    — RPS-кольцо, p95/p99, Prometheus-экспозиция
generator/main.go    — нагрузочный генератор
c_lib/, rust_lib/    — исходные нативные библиотеки (без изменений)
build.sh             — исходный скрипт сборки .so (без изменений)
```

## Сборка и запуск

```bash
# 1. Собрать нативные библиотеки (как раньше) — `make libs` дополнительно
#    копирует .so в bin/ рядом с бинарником сервера
make libs
# или вручную: ./build.sh

# 2. Собрать Go-бинарники (работает и без .so — включится Go-fallback)
make build
# или вручную:
# go build -o bin/calculator_server ./server
# go build -o bin/generator ./generator

# 3. Запустить
./bin/calculator_server --port 8080
./bin/generator -n 20 --interval 0
# или: make run-server / make run-gen (параметры: PORT, GEN_URL, GEN_THREADS, ...)

# 4. Юнит-тесты (16 тестов: парсинг query, RPS-кольцо, квантили,
#    хендлеры, конкурентный инвариант sum+sub==0, воркеры генератора)
make tests   # все проверки разом: build + fmt + vet + test + test-race
# по отдельности: make test / make test-race / make vet / make fmt
# Полный список целей: make help (libs, build, clean, run-server, run-gen, ...)
```

Флаги сервера повторяют Python-версию: `--host`, `--port`, `--c-lib`,
`--rust-lib`, `--interval`. Относительные пути к `.so` ищутся сначала в
текущем каталоге, затем рядом с бинарником (`bin/*.so`), поэтому сервер
находит библиотеки независимо от каталога запуска. Флаги генератора:
`--url`, `-n/--threads`, `--interval`, `--timeout`. Поведение при ошибках
и тексты ответов
(`missing 'num' query parameter`, `'num' must be an integer`, `not found`)
сохранены.

## Метрики: GET /metrics

```text
calculator_http_rps{age_seconds="0..59"}     — запросы к /calc за каждую из последних 60 секунд
calculator_http_requests_total               — всего успешных POST /calc
calculator_c_call_duration_seconds{quantile="0.95"|"0.99"}    — p95/p99 вызова C add(), секунды
calculator_rust_call_duration_seconds{quantile="0.95"|"0.99"} — p95/p99 вызова Rust sub(), секунды
calculator_c_calls_total / calculator_rust_calls_total        — счётчики вызовов
calculator_backend_info{backend="c"|"rust",impl="native"|"go_fallback"} — какой бэкенд активен
```

Окна: RPS — кольцо из 61 посекундного счётчика; p95/p99 — скользящее окно
последних 16384 вызовов каждого бэкенда (снимок копируется и сортируется
только в момент scrape, горячий путь не блокируется).

## Принятые решения (для обсуждения на собеседовании)

1. **Lock-free состояние.** `sum += num` и `sub -= num` коммутативны, поэтому
   вместо мьютекса из Python-версии используются `atomic.Int64`. Финальные
   суммы бит-в-бит совпадают с последовательным исполнением, но запросы
   больше не сериализуются. Проверено инвариантом `sum + sub == 0` после
   ~860k конкурентных запросов и прогоном под `-race`.
2. **C и Rust вызываются последовательно в горутине запроса.** Работа
   CPU-bound и одинакова в сумме; две горутины на запрос лишь добавили бы
   накладных расходов планировщика без роста суммарного RPS.
3. **Нативные библиотеки грузятся через dlopen в рантайме** (аналог
   `ctypes.CDLL`, пути — через `--c-lib/--rust-lib`). Замер времени идёт
   вокруг вызова и включает переход cgo — честные цифры. Если `.so` нет
   (или символа в нём), сервер работает на встроенной Go-реализации с той
   же семантикой и помечает это в `calculator_backend_info` — бинарник
   собирается и запускается везде.
4. **Горячий путь `/calc` без аллокаций по возможности:** тело не читается
   (num в query, как в Python), `num` парсится ручным сканом `RawQuery`
   без `url.Values`-map, ответ — статический `ok`, логгирования на запрос нет.
5. **Метрики не тормозят запросы:** RPS — один atomic add (CAS только на
   границе секунды), латентности — один atomic add + store в кольцо.
   Сортировка только при scrape `/metrics`.
6. **Генератор:** общий `http.Client` с keep-alive (`MaxIdleConnsPerHost = N`),
   собственный `rand.Source` на воркер без общего мьютекса.

## Замеры (8-ядерная Linux-машина, нативные .so)

| Сценарий | RPS |
|---|---|
| Python `calculator_server.py` + Go-генератор (20 потоков) | ~2 800 |
| Go `calculator_server` + Go-генератор (20 потоков, interval 0) | ~96 000 |

Ускорение ~34x; ноль ошибок на ~860k запросов. p95 вызова C под нагрузкой
~22 мкс, p99 ~46 мкс (плавают от частоты CPU).

