\set ON_ERROR_STOP on
\pset null '(null)'

\echo
\echo '=== Jeu de données : products et products_archive ==='
CREATE TABLE products (
    id           int PRIMARY KEY,
    name         text NOT NULL,
    price        numeric(10, 2) NOT NULL,
    discontinued boolean NOT NULL DEFAULT false
);
CREATE TABLE products_archive (LIKE products);

INSERT INTO products VALUES
    (1, 'clavier', 40.00, false),
    (2, 'souris', 20.00, false),
    (3, 'ecran', 150.00, true);

\echo
\echo '=== WITH ... MATERIALIZED ==='
WITH discontinued_products AS MATERIALIZED (
    SELECT id, price FROM products WHERE discontinued
)
SELECT count(*), sum(price) FROM discontinued_products;

\echo
\echo '=== UPDATE ... RETURNING old / new (PostgreSQL 18) ==='
UPDATE products SET price = price * 1.05
WHERE id = 1
RETURNING old.price AS old_price, new.price AS new_price;

\echo
\echo '=== UPDATE ... FROM : price_changes avec une ligne par produit ==='
CREATE TABLE price_changes (product_id int, new_price numeric(10, 2));
INSERT INTO price_changes VALUES (1, 42.00), (2, 18.50);

UPDATE products p
SET price = c.new_price
FROM price_changes c
WHERE c.product_id = p.id;

SELECT id, name, price FROM products ORDER BY id;

\echo
\echo '=== UPDATE ... FROM : doublon dans price_changes (une seule ligne jointe est appliquée) ==='
BEGIN;
UPDATE products SET price = 40.00 WHERE id = 1;
CREATE TEMP TABLE price_changes_dup (product_id int, new_price numeric(10, 2));
INSERT INTO price_changes_dup VALUES (1, 43.00), (1, 45.00);

UPDATE products p
SET price = c.new_price
FROM price_changes_dup c
WHERE c.product_id = p.id
RETURNING p.id, c.new_price AS applied_price;

SELECT id, name, price FROM products WHERE id = 1;
ROLLBACK;

\echo
\echo '=== CTE modifiante : DELETE ... RETURNING puis INSERT dans products_archive ==='
WITH moved AS (
    DELETE FROM products
    WHERE discontinued
    RETURNING *
)
INSERT INTO products_archive
SELECT * FROM moved;

\echo --- products
SELECT * FROM products ORDER BY id;
\echo --- products_archive
SELECT * FROM products_archive ORDER BY id;

\echo
\echo '=== Remise à zéro des données avant les variantes du snapshot ==='
TRUNCATE products, products_archive;
INSERT INTO products VALUES
    (1, 'clavier', 40.00, false),
    (2, 'souris', 20.00, false),
    (3, 'ecran', 150.00, true);

\echo
\echo '=== Snapshot partagé, variante 1 : SELECT FROM products (anciens prix attendus) ==='
BEGIN;
WITH t AS (
    UPDATE products SET price = price * 1.05
    RETURNING *
)
SELECT * FROM products;
\echo --- état de la table après la requête, dans la même transaction
SELECT id, price FROM products ORDER BY id;
ROLLBACK;

\echo
\echo '=== Snapshot partagé, variante 2 : SELECT FROM t (nouveaux prix attendus) ==='
BEGIN;
WITH t AS (
    UPDATE products SET price = price * 1.05
    RETURNING *
)
SELECT * FROM t;
ROLLBACK;

\echo
\echo '=== Index unique partiel : tasks_pending_target_uq ==='
CREATE TABLE tasks (
    id     serial PRIMARY KEY,
    target text NOT NULL,
    status text NOT NULL
);
CREATE UNIQUE INDEX tasks_pending_target_uq
    ON tasks (target) WHERE status = 'PENDING';

\set VERBOSITY verbose
\set ON_ERROR_STOP off

\echo '--- INSERT 1 (PENDING) : réussit'
INSERT INTO tasks (target, status) VALUES ('plan-a', 'PENDING');
\echo '--- INSERT 2 (RUNNING) : réussit'
INSERT INTO tasks (target, status) VALUES ('plan-a', 'RUNNING');
\echo '--- INSERT 3 (PENDING) : échoue, SQLSTATE 23505 attendu'
INSERT INTO tasks (target, status) VALUES ('plan-a', 'PENDING');
\echo '--- SQLSTATE de la dernière erreur'
\echo :LAST_ERROR_SQLSTATE
\echo '--- UPDATE RUNNING -> PENDING : échoue, SQLSTATE 23505 attendu'
UPDATE tasks SET status = 'PENDING' WHERE target = 'plan-a' AND status = 'RUNNING';
\echo :LAST_ERROR_SQLSTATE

\set ON_ERROR_STOP on
\set VERBOSITY default

\echo '--- contenu final de tasks'
SELECT * FROM tasks ORDER BY id;
