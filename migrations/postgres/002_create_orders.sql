CREATE TABLE orders (
    id         BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer   VARCHAR(255) NOT NULL,
    subtotal   BIGINT       NOT NULL, -- all amounts are cents
    discount   BIGINT       NOT NULL,
    total      BIGINT       NOT NULL,
    created_at TIMESTAMP(6) NOT NULL
);

CREATE TABLE order_items (
    id       BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id BIGINT       NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    line_no  INT          NOT NULL,
    name     VARCHAR(255) NOT NULL,
    qty      INT          NOT NULL,
    price    BIGINT       NOT NULL -- per unit, cents
);

CREATE INDEX idx_order_items_order ON order_items (order_id);
