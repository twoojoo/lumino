DROP DATABASE IF EXISTS lumino_test;
CREATE DATABASE lumino_test CHARACTER SET utf8mb4;

CREATE TABLE lumino_test.customers (
    id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name varchar(100) NOT NULL,
    email varchar(200) UNIQUE,
    created_at datetime DEFAULT CURRENT_TIMESTAMP
) COMMENT 'People who buy things';

CREATE TABLE lumino_test.orders (
    id int NOT NULL AUTO_INCREMENT PRIMARY KEY,
    customer_id int NOT NULL,
    amount decimal(10,2) NOT NULL DEFAULT 0,
    status varchar(20) NOT NULL CHECK (status IN ('new', 'paid', 'shipped')),
    note text,
    CONSTRAINT orders_customer_fk FOREIGN KEY (customer_id) REFERENCES lumino_test.customers (id) ON DELETE CASCADE
);
CREATE INDEX orders_status_idx ON lumino_test.orders (status);

CREATE TABLE lumino_test.order_items (
    order_id int NOT NULL,
    line_no int NOT NULL,
    sku varchar(40) NOT NULL,
    qty int NOT NULL DEFAULT 1,
    PRIMARY KEY (order_id, line_no),
    FOREIGN KEY (order_id) REFERENCES lumino_test.orders (id) ON DELETE CASCADE
);

CREATE TABLE lumino_test.all_types (
    id int PRIMARY KEY,
    c_int int,
    c_bigint bigint,
    c_dec decimal(12,4),
    c_float double,
    c_bool boolean,
    c_text text,
    c_varchar varchar(50),
    c_date date,
    c_time time,
    c_ts datetime(6),
    c_tstz timestamp(6) NULL,
    c_bytes varbinary(20),
    c_json json,
    c_uuid char(36),
    c_enum enum('a', 'b', 'c'),
    c_ubig bigint unsigned
);
INSERT INTO lumino_test.all_types (id) VALUES (1);

CREATE TABLE lumino_test.ddl_rt (
    id bigint NOT NULL AUTO_INCREMENT,
    code varchar(10) NOT NULL DEFAULT 'x',
    price decimal(8,2) CHECK (price >= 0),
    doubled decimal(9,2) GENERATED ALWAYS AS (price * 2) VIRTUAL,
    customer_id int,
    PRIMARY KEY (id),
    UNIQUE KEY ddl_rt_code_uq (code),
    KEY ddl_rt_price_idx (price, code),
    CONSTRAINT ddl_rt_fk FOREIGN KEY (customer_id) REFERENCES lumino_test.customers (id) ON UPDATE CASCADE ON DELETE SET NULL
) COMMENT 'round trip';

CREATE TABLE lumino_test.digits (d int PRIMARY KEY);
INSERT INTO lumino_test.digits VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9);
CREATE TABLE lumino_test.big (id int PRIMARY KEY, label varchar(30), v double);
INSERT INTO lumino_test.big
SELECT 1 + a.d + 10*b.d + 100*c.d + 1000*e.d + 10000*f.d, CONCAT('row ', 1 + a.d + 10*b.d + 100*c.d + 1000*e.d + 10000*f.d),
       (1 + a.d + 10*b.d + 100*c.d + 1000*e.d + 10000*f.d) * 0.5
FROM lumino_test.digits a, lumino_test.digits b, lumino_test.digits c, lumino_test.digits e, lumino_test.digits f;

INSERT INTO lumino_test.customers (id, name, email) VALUES (1, 'Ada', 'ada@example.com'), (2, 'Linus', 'linus@example.com'), (3, 'a;b', NULL);
INSERT INTO lumino_test.orders (id, customer_id, amount, status) VALUES (1, 1, 10.50, 'new'), (2, 2, 99.99, 'paid');
INSERT INTO lumino_test.order_items VALUES (1, 1, 'SKU-1', 3);

CREATE VIEW lumino_test.customer_totals AS
SELECT c.id, c.name, COALESCE(SUM(o.amount), 0) AS total FROM lumino_test.customers c LEFT JOIN lumino_test.orders o ON o.customer_id = c.id GROUP BY c.id, c.name;

DELIMITER //
CREATE FUNCTION lumino_test.add_tax(amount decimal(10,2)) RETURNS decimal(10,2) DETERMINISTIC
BEGIN
    DECLARE r decimal(10,2);
    SET r = amount * 1.22; -- a ; inside
    RETURN r;
END//
DELIMITER ;

CREATE PROCEDURE lumino_test.reset_notes()
BEGIN
    UPDATE lumino_test.orders SET note = NULL;
    IF (SELECT COUNT(*) FROM lumino_test.orders) > 0 THEN
        SELECT 'done';
    END IF;
END;

CREATE TRIGGER lumino_test.orders_touch BEFORE INSERT ON lumino_test.orders FOR EACH ROW
BEGIN
    IF NEW.note IS NULL THEN SET NEW.note = 'touched'; END IF;
END;

CREATE SEQUENCE lumino_test.invoice_seq START WITH 1000 INCREMENT BY 5;
