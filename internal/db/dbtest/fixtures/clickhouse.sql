DROP DATABASE IF EXISTS lumino_test;
CREATE DATABASE lumino_test;

CREATE TABLE lumino_test.customers (
    id Int32,
    name String,
    email Nullable(String),
    created_at DateTime DEFAULT now()
) ENGINE = MergeTree ORDER BY id COMMENT 'People who buy things';

CREATE TABLE lumino_test.orders (
    id Int32,
    customer_id Int32,
    amount Decimal(10,2) DEFAULT 0,
    status LowCardinality(String),
    note Nullable(String),
    INDEX orders_status_idx status TYPE set(10) GRANULARITY 1
) ENGINE = MergeTree ORDER BY id;

CREATE TABLE lumino_test.order_items (
    order_id Int32,
    line_no Int32,
    sku String,
    qty Int32 DEFAULT 1
) ENGINE = MergeTree ORDER BY (order_id, line_no);

CREATE TABLE lumino_test.all_types (
    id Int32,
    c_int Nullable(Int32),
    c_bigint Nullable(Int64),
    c_dec Nullable(Decimal(12,4)),
    c_float Nullable(Float64),
    c_bool Nullable(Bool),
    c_text Nullable(String),
    c_varchar LowCardinality(Nullable(String)),
    c_date Nullable(Date),
    c_ts Nullable(DateTime64(6)),
    c_tstz Nullable(DateTime64(6, 'Europe/Rome')),
    c_uuid Nullable(UUID),
    c_arr Array(Int32),
    c_ubig UInt64
) ENGINE = MergeTree ORDER BY id;
INSERT INTO lumino_test.all_types (id) VALUES (1);

CREATE TABLE lumino_test.ddl_rt (
    id UInt64,
    code String DEFAULT 'x',
    price Nullable(Decimal(8,2)),
    doubled Nullable(Decimal(9,2)) MATERIALIZED price * 2,
    day Date,
    INDEX ddl_rt_code_idx code TYPE bloom_filter GRANULARITY 4
) ENGINE = ReplacingMergeTree PARTITION BY toYYYYMM(day) ORDER BY (id, code) TTL day + INTERVAL 10 YEAR
COMMENT 'round trip';

CREATE TABLE lumino_test.big (id UInt32, label String, v Float64) ENGINE = MergeTree ORDER BY id;
INSERT INTO lumino_test.big SELECT number + 1, concat('row ', toString(number + 1)), (number + 1) * 0.5 FROM numbers(100000);

INSERT INTO lumino_test.customers (id, name, email) VALUES (1, 'Ada', 'ada@example.com'), (2, 'Linus', 'linus@example.com'), (3, 'a;b', NULL);
INSERT INTO lumino_test.orders (id, customer_id, amount, status) VALUES (1, 1, 10.50, 'new'), (2, 2, 99.99, 'paid');
INSERT INTO lumino_test.order_items VALUES (1, 1, 'SKU-1', 3);

CREATE VIEW lumino_test.customer_totals AS
SELECT c.id, c.name, sum(o.amount) AS total FROM lumino_test.customers c LEFT JOIN lumino_test.orders o ON o.customer_id = c.id GROUP BY c.id, c.name;

CREATE MATERIALIZED VIEW lumino_test.order_stats ENGINE = SummingMergeTree ORDER BY status POPULATE AS
SELECT status, count() AS n FROM lumino_test.orders GROUP BY status;
