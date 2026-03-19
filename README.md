# ai-model

Сервис для автоматического triage падений автотестов из Allure TestOps:

- выгружает запуски и тест-результаты из Allure в Postgres/Timescale;
- строит knowledge base по прошлым падениям и дефектам;
- находит релевантные кейсы через history match, lexical search и опциональный semantic rerank;
- принимает решение `rerun` / `attach_existing_bug` / `create_bug`;
- формирует заготовку описания бага со ссылками на запуск и тест.

## Текущее состояние

В репозитории собран рабочий CLI:

- `migrate` применяет схему БД;
- `sync` синхронизирует один запуск или последние N запусков;
- `triage` анализирует упавшие тесты в запуске и сохраняет решения в БД.

`results-exporter` в соседнем репозитории остаётся полезным только как черновик по API Allure. Основной путь сейчас должен идти через этот сервис.

## Архитектура

Поток данных:

1. `sync` получает launch/test result/retries/defects из Allure.
2. Нормализованные данные сохраняются в таблицы `launches`, `test_results`, `defects`, `test_result_defects`.
3. Для упавших тестов и дефектов формируются `knowledge_documents`.
4. `triage` по каждому падению:
   - считает историю прохождений;
   - ищет exact historical matches;
   - делает lexical retrieval по knowledge base;
   - при наличии эмбеддингов и semantic view делает semantic rerank;
   - принимает решение правилами или через LLM поверх подготовленных кандидатов.

## Почему так

- История прохождений и дефекты разделены: pass/fail статистика живёт в `test_results`, retrieval-корпус в `knowledge_documents`.
- Exact/history retrieval всегда идёт первым: это самый точный источник для автотестов.
- Семантика сделана опциональной: без неё сервис уже полезен и локально запускается проще.
- Конфиг через env, без захардкоженных секретов.
- В базовый конфиг подтянут `go-base`.

## Allure API

Сервис поддерживает два варианта авторизации:

- `ALLURE_BEARER_TOKEN`
- `ALLURE_USER_TOKEN`

Если передан `ALLURE_USER_TOKEN`, сервис сначала меняет его на bearer через `/api/uaa/oauth/token`.

Для уточнения конкретных endpoint'ов по созданию/линковке дефектов в вашем инстансе нужно смотреть swagger:

- `<ALLURE_BASE_URL>/swagger-ui.html`

По публичной документации Allure TestOps API swagger доступен именно там. В этой реализации создание дефекта через API пока не включено, но уже формируется содержимое для будущего запроса в Allure/Jira.

## Semantic search и pgai

Базовый режим работает без pgai.

Если хотите semantic search в БД:

1. Поднимите Postgres/Timescale.
2. Храните исходные документы в `knowledge_documents`.
3. Настройте pgai vectorizer на колонку `content`.
4. Укажите имя generated view в `ANALYSIS_SEMANTIC_VIEW_NAME`.

Важно: по состоянию на 26 февраля 2026 репозиторий `timescale/pgai` переведён в archived. Поэтому использовать его можно, но я бы рассматривал это как опциональный слой, а не единственную критичную зависимость системы.

## Локальный запуск

### 1. Поднять локальный стенд

Локальная инфраструктура теперь живёт в соседнем репозитории `local-debug`.

```bash
cd ../local-debug
docker compose up -d
```

### 2. Заполнить env

Скопируйте `.env.example` в `.env` и задайте значения.

Минимально нужны:

```env
DATABASE_URL=postgres://postgres:postgres@localhost:5432/autotest_ai?sslmode=disable
ALLURE_BASE_URL=https://allure.services.mts.ru
ALLURE_PROJECT_ID=271
ALLURE_USER_TOKEN=...
```

Для LLMOps MTS можно использовать OpenAI-compatible конфиг:

```env
LLM_ENABLED=true
LLM_BASE_URL=https://api.llmops.mts-corp.ru/v1
LLM_API_KEY=...
LLM_MODEL=mts-anya
```

Для эмбеддингов аналогично:

```env
EMBEDDINGS_ENABLED=true
EMBEDDINGS_BASE_URL=https://api.llmops.mts-corp.ru/v1
EMBEDDINGS_API_KEY=...
EMBEDDINGS_MODEL=text-embedding-3-small
```

Если локально поднимете OpenAI-compatible шлюз над Hugging Face/vLLM, сервис можно переключить на него теми же переменными.

### 3. Применить схему

```bash
go run ./cmd/assistant migrate
```

### 4. Синхронизировать данные

Последние 20 запусков:

```bash
go run ./cmd/assistant sync --recent 20
```

Один запуск:

```bash
go run ./cmd/assistant sync --launch-id 123456
```

### 5. Запустить triage

```bash
go run ./cmd/assistant triage --launch-id 123456
```

CLI вернёт JSON-решения и одновременно сохранит их в `triage_decisions`.

## Важные env-переменные

### Database

- `DATABASE_URL`
- `DATABASE_MIN_CONNS`
- `DATABASE_MAX_CONNS`

### Allure

- `ALLURE_BASE_URL`
- `ALLURE_PROJECT_ID`
- `ALLURE_USER_TOKEN`
- `ALLURE_BEARER_TOKEN`
- `ALLURE_PAGE_SIZE`
- `ALLURE_SYNC_LAUNCH_LIMIT`
- `ALLURE_LAUNCH_URL_TEMPLATE`
- `ALLURE_TEST_URL_TEMPLATE`

### LLM

- `LLM_ENABLED`
- `LLM_BASE_URL`
- `LLM_API_KEY`
- `LLM_MODEL`
- `LLM_TIMEOUT`
- `LLM_TEMPERATURE`

### Embeddings

- `EMBEDDINGS_ENABLED`
- `EMBEDDINGS_BASE_URL`
- `EMBEDDINGS_API_KEY`
- `EMBEDDINGS_MODEL`
- `EMBEDDINGS_TIMEOUT`

### Analysis

- `ANALYSIS_TOP_K_PER_QUERY`
- `ANALYSIS_MAX_CANDIDATES`
- `ANALYSIS_HISTORY_WINDOW`
- `ANALYSIS_STRONG_DEFECT_THRESHOLD`
- `ANALYSIS_STRONG_RERUN_PASS_RATE`
- `ANALYSIS_ATTACH_CANDIDATE_MINIMUM`
- `ANALYSIS_SEMANTIC_SEARCH_ENABLED`
- `ANALYSIS_SEMANTIC_VIEW_NAME`

## Что дальше стоит сделать

1. Добавить write-back в Allure defect API и отдельный Jira adapter.
2. Подтвердить реальные UI/API шаблоны ссылок на launch/test result в вашем инстансе Allure.
3. Завести отдельную таблицу feedback, чтобы хранить ручные коррекции triage и дообучать/калибровать правила.
4. Если semantic retrieval станет критичен, принять отдельное решение по pgai vs pgvector+app-side embeddings vs Qdrant.

## Проверка

```bash
go test ./...
```
