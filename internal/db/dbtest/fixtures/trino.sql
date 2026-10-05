DROP VIEW IF EXISTS memory.lumino_test.customer_totals;
DROP TABLE IF EXISTS memory.lumino_test.customers;
DROP TABLE IF EXISTS memory.lumino_test.orders;
DROP TABLE IF EXISTS memory.lumino_test.order_items;
DROP TABLE IF EXISTS memory.lumino_test.all_types;
DROP TABLE IF EXISTS memory.lumino_test.ddl_rt;
DROP TABLE IF EXISTS memory.lumino_test.big;
DROP SCHEMA IF EXISTS memory.lumino_test;
CREATE SCHEMA memory.lumino_test;

CREATE TABLE memory.lumino_test.customers (id integer, name varchar(100), email varchar(200), created_at timestamp(3)) COMMENT 'People who buy things';
CREATE TABLE memory.lumino_test.orders (id integer, customer_id integer, amount decimal(10,2), status varchar(20), note varchar);
CREATE TABLE memory.lumino_test.order_items (order_id integer, line_no integer, sku varchar(40), qty integer);
CREATE TABLE memory.lumino_test.all_types (id integer, c_int integer, c_bigint bigint, c_dec decimal(12,4), c_float double, c_bool boolean,
    c_text varchar, c_date date, c_ts timestamp(6), c_tstz timestamp(6) with time zone, c_bytes varbinary, c_json json, c_uuid uuid, c_arr array(integer));
INSERT INTO memory.lumino_test.all_types VALUES (1, 42, 9007199254740993, DECIMAL '12345678.1234', 3.25e0, true, 'héllo', DATE '2024-02-29',
    TIMESTAMP '2024-02-29 13:14:15.123456', TIMESTAMP '2024-02-29 13:14:15.123456 +02:00', X'00ff10', JSON '{"a": [1, 2]}',
    UUID '8f3e1c2a-4b5d-4e6f-8a9b-0c1d2e3f4a5b', ARRAY[1, 2, 3]);
CREATE TABLE memory.lumino_test.ddl_rt (id bigint, code varchar(10), price decimal(8,2)) COMMENT 'round trip';
CREATE TABLE memory.lumino_test.big AS SELECT CAST(a.x * 10000 + b.y AS integer) AS id, 'row ' || CAST(a.x * 10000 + b.y AS varchar) AS label, 0.5e0 AS v
FROM UNNEST(sequence(0, 9)) AS a(x) CROSS JOIN UNNEST(sequence(1, 10000)) AS b(y);

INSERT INTO memory.lumino_test.customers (id, name, email) VALUES (1, 'Ada', 'ada@example.com'), (2, 'Linus', 'linus@example.com'), (3, 'a;b', NULL);
INSERT INTO memory.lumino_test.orders (id, customer_id, amount, status) VALUES (1, 1, 10.50, 'new'), (2, 2, 99.99, 'paid');
INSERT INTO memory.lumino_test.order_items VALUES (1, 1, 'SKU-1', 3);

CREATE VIEW memory.lumino_test.customer_totals AS
SELECT c.id, c.name, COALESCE(sum(o.amount), 0) AS total FROM memory.lumino_test.customers c LEFT JOIN memory.lumino_test.orders o ON o.customer_id = c.id GROUP BY c.id, c.name;
