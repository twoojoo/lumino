IF OBJECT_ID('lumino_test.customer_totals', 'V') IS NOT NULL DROP VIEW lumino_test.customer_totals;
IF OBJECT_ID('lumino_test.reset_notes', 'P') IS NOT NULL DROP PROCEDURE lumino_test.reset_notes;
IF OBJECT_ID('lumino_test.add_tax', 'FN') IS NOT NULL DROP FUNCTION lumino_test.add_tax;
IF OBJECT_ID('lumino_test.invoice_seq', 'SO') IS NOT NULL DROP SEQUENCE lumino_test.invoice_seq;
DROP TABLE IF EXISTS lumino_test.order_items, lumino_test.ddl_rt, lumino_test.orders, lumino_test.customers, lumino_test.all_types, lumino_test.big, lumino_test.digits;
IF SCHEMA_ID('lumino_test') IS NOT NULL DROP SCHEMA lumino_test;
GO
CREATE SCHEMA lumino_test;
GO

CREATE TABLE lumino_test.customers (
    id int NOT NULL PRIMARY KEY,
    name nvarchar(100) NOT NULL,
    email nvarchar(200) NULL UNIQUE,
    created_at datetime2 DEFAULT SYSDATETIME()
);

CREATE TABLE lumino_test.orders (
    id int IDENTITY(1,1) NOT NULL PRIMARY KEY,
    customer_id int NOT NULL REFERENCES lumino_test.customers (id) ON DELETE CASCADE,
    amount decimal(10,2) NOT NULL DEFAULT 0,
    status varchar(20) NOT NULL CHECK (status IN ('new', 'paid', 'shipped')),
    note nvarchar(max) NULL
);
CREATE INDEX orders_status_idx ON lumino_test.orders (status);

CREATE TABLE lumino_test.order_items (
    order_id int NOT NULL REFERENCES lumino_test.orders (id) ON DELETE CASCADE,
    line_no int NOT NULL,
    sku varchar(40) NOT NULL,
    qty int NOT NULL DEFAULT 1,
    PRIMARY KEY (order_id, line_no)
);

CREATE TABLE lumino_test.all_types (
    id int NOT NULL PRIMARY KEY,
    c_int int NULL,
    c_bigint bigint NULL,
    c_dec decimal(12,4) NULL,
    c_float float NULL,
    c_bool bit NULL,
    c_text nvarchar(max) NULL,
    c_varchar varchar(50) NULL,
    c_date date NULL,
    c_time time(6) NULL,
    c_ts datetime2(6) NULL,
    c_tstz datetimeoffset(6) NULL,
    c_bytes varbinary(20) NULL,
    c_uuid uniqueidentifier NULL,
    c_money money NULL
);
INSERT INTO lumino_test.all_types (id) VALUES (1);

CREATE TABLE lumino_test.ddl_rt (
    id bigint IDENTITY(10,5) NOT NULL,
    code varchar(10) NOT NULL CONSTRAINT ddl_rt_code_df DEFAULT 'x',
    price decimal(8,2) NULL CONSTRAINT ddl_rt_price_ck CHECK (price >= 0),
    doubled AS (price * 2),
    customer_id int NULL,
    CONSTRAINT ddl_rt_pk PRIMARY KEY CLUSTERED (id),
    CONSTRAINT ddl_rt_code_uq UNIQUE (code),
    CONSTRAINT ddl_rt_fk FOREIGN KEY (customer_id) REFERENCES lumino_test.customers (id) ON UPDATE CASCADE ON DELETE SET NULL
);
CREATE INDEX ddl_rt_price_idx ON lumino_test.ddl_rt (price) WHERE price > 10;

CREATE TABLE lumino_test.digits (d int PRIMARY KEY);
INSERT INTO lumino_test.digits VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9);
CREATE TABLE lumino_test.big (id int PRIMARY KEY, label varchar(30), v float);
INSERT INTO lumino_test.big
SELECT 1 + a.d + 10*b.d + 100*c.d + 1000*e.d + 10000*f.d, CONCAT('row ', 1 + a.d + 10*b.d + 100*c.d + 1000*e.d + 10000*f.d),
       (1 + a.d + 10*b.d + 100*c.d + 1000*e.d + 10000*f.d) * 0.5
FROM lumino_test.digits a, lumino_test.digits b, lumino_test.digits c, lumino_test.digits e, lumino_test.digits f;

INSERT INTO lumino_test.customers (id, name, email) VALUES (1, 'Ada', 'ada@example.com'), (2, 'Linus', 'linus@example.com'), (3, 'a;b', 'ab@example.com');
SET IDENTITY_INSERT lumino_test.orders ON;
INSERT INTO lumino_test.orders (id, customer_id, amount, status) VALUES (1, 1, 10.50, 'new'), (2, 2, 99.99, 'paid');
SET IDENTITY_INSERT lumino_test.orders OFF;
INSERT INTO lumino_test.order_items VALUES (1, 1, 'SKU-1', 3);
GO

CREATE VIEW lumino_test.customer_totals AS
SELECT c.id, c.name, COALESCE(SUM(o.amount), 0) AS total FROM lumino_test.customers c LEFT JOIN lumino_test.orders o ON o.customer_id = c.id GROUP BY c.id, c.name;
GO

CREATE FUNCTION lumino_test.add_tax(@amount decimal(10,2)) RETURNS decimal(10,2)
AS
BEGIN
    DECLARE @r decimal(10,2);
    SET @r = @amount * 1.22; -- a ; inside
    RETURN @r;
END
GO

CREATE PROCEDURE lumino_test.reset_notes
AS
    SET NOCOUNT ON;
    UPDATE lumino_test.orders SET note = NULL;
    SELECT COUNT(*) AS n FROM lumino_test.orders;
GO

CREATE TRIGGER lumino_test.orders_touch ON lumino_test.orders AFTER INSERT
AS
BEGIN
    SET NOCOUNT ON;
    UPDATE o SET note = 'touched' FROM lumino_test.orders o JOIN inserted i ON i.id = o.id WHERE o.note IS NULL;
END
GO

CREATE SEQUENCE lumino_test.invoice_seq AS bigint START WITH 1000 INCREMENT BY 5;
