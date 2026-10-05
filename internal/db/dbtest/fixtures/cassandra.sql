DROP KEYSPACE IF EXISTS lumino_test;
CREATE KEYSPACE lumino_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1};

CREATE TABLE lumino_test.customers (
    id int PRIMARY KEY,
    name text,
    email text,
    created_at timestamp
) WITH comment = 'People who buy things';

CREATE TABLE lumino_test.orders (
    id int PRIMARY KEY,
    customer_id int,
    amount decimal,
    status text,
    note text
);
CREATE INDEX orders_status_idx ON lumino_test.orders (status);

CREATE TABLE lumino_test.order_items (
    order_id int,
    line_no int,
    sku text,
    qty int,
    PRIMARY KEY ((order_id), line_no)
) WITH CLUSTERING ORDER BY (line_no DESC);

CREATE TABLE lumino_test.all_types (
    id int PRIMARY KEY,
    c_int int,
    c_bigint bigint,
    c_dec decimal,
    c_float double,
    c_bool boolean,
    c_text text,
    c_varchar varchar,
    c_date date,
    c_ts timestamp,
    c_bytes blob,
    c_uuid uuid,
    c_list list<int>,
    c_inet inet,
    c_varint varint
);
INSERT INTO lumino_test.all_types (id) VALUES (1);

CREATE TABLE lumino_test.ddl_rt (
    tenant text,
    id timeuuid,
    code text,
    price decimal,
    tags set<text>,
    meta map<text, int>,
    shared text STATIC,
    PRIMARY KEY ((tenant), id)
) WITH CLUSTERING ORDER BY (id DESC) AND comment = 'round trip';

CREATE TABLE lumino_test.big (id int PRIMARY KEY, label text, v double);

INSERT INTO lumino_test.customers (id, name, email) VALUES (1, 'Ada', 'ada@example.com');
INSERT INTO lumino_test.customers (id, name, email) VALUES (2, 'Linus', 'linus@example.com');
INSERT INTO lumino_test.customers (id, name) VALUES (3, 'a;b');
BEGIN BATCH
INSERT INTO lumino_test.orders (id, customer_id, amount, status) VALUES (1, 1, 10.50, 'new');
INSERT INTO lumino_test.orders (id, customer_id, amount, status) VALUES (2, 2, 99.99, 'paid');
APPLY BATCH;
INSERT INTO lumino_test.order_items (order_id, line_no, sku, qty) VALUES (1, 1, 'SKU-1', 3);

CREATE MATERIALIZED VIEW lumino_test.orders_by_status AS
SELECT * FROM lumino_test.orders WHERE status IS NOT NULL AND id IS NOT NULL PRIMARY KEY (status, id);
