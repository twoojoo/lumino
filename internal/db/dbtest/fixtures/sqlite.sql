PRAGMA foreign_keys = OFF;
DROP VIEW IF EXISTS customer_totals;
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS ddl_rt;
DROP TABLE IF EXISTS customers;
DROP TABLE IF EXISTS all_types;
DROP TABLE IF EXISTS big;
PRAGMA foreign_keys = ON;

CREATE TABLE customers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name VARCHAR(100) NOT NULL,
    email VARCHAR(200) UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE orders (
    id INTEGER PRIMARY KEY,
    customer_id INTEGER NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
    amount DECIMAL(10,2) NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL CHECK (status IN ('new', 'paid', 'shipped')),
    note TEXT
);
CREATE INDEX orders_status_idx ON orders (status);

CREATE TABLE order_items (
    order_id INTEGER NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    line_no INTEGER NOT NULL,
    sku TEXT NOT NULL,
    qty INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (order_id, line_no)
);

CREATE TABLE all_types (
    id INTEGER PRIMARY KEY,
    c_int INTEGER,
    c_bigint BIGINT,
    c_dec DECIMAL(12,4),
    c_float REAL,
    c_bool BOOLEAN,
    c_text TEXT,
    c_varchar VARCHAR(50),
    c_date DATE,
    c_ts DATETIME,
    c_bytes BLOB,
    c_json JSON
);
INSERT INTO all_types (id) VALUES (1);

CREATE TABLE ddl_rt (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code TEXT NOT NULL DEFAULT 'x' UNIQUE,
    price NUMERIC CHECK (price >= 0),
    doubled NUMERIC GENERATED ALWAYS AS (price * 2) VIRTUAL,
    customer_id INTEGER REFERENCES customers (id) ON UPDATE CASCADE ON DELETE SET NULL
);
CREATE INDEX ddl_rt_lower_idx ON ddl_rt (lower(code)) WHERE price > 10;

CREATE TABLE big (id INTEGER PRIMARY KEY, label TEXT, v REAL);
WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 100000)
INSERT INTO big SELECT n, 'row ' || n, n * 0.5 FROM g;

INSERT INTO customers (id, name, email) VALUES (1, 'Ada', 'ada@example.com'), (2, 'Linus', 'linus@example.com'), (3, 'a;b', NULL);
INSERT INTO orders (id, customer_id, amount, status) VALUES (1, 1, 10.50, 'new'), (2, 2, 99.99, 'paid');
INSERT INTO order_items VALUES (1, 1, 'SKU-1', 3);

CREATE VIEW customer_totals AS
SELECT c.id, c.name, COALESCE(SUM(o.amount), 0) AS total FROM customers c LEFT JOIN orders o ON o.customer_id = c.id GROUP BY c.id, c.name;

CREATE TRIGGER orders_touch AFTER INSERT ON orders FOR EACH ROW WHEN NEW.note IS NULL
BEGIN
    UPDATE orders SET note = 'touched' WHERE id = NEW.id;
END;
