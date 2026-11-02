CREATE TABLE orders (
    id         BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    customer   VARCHAR(255) NOT NULL,
    subtotal   BIGINT       NOT NULL, -- all amounts are cents
    discount   BIGINT       NOT NULL,
    total      BIGINT       NOT NULL,
    created_at DATETIME(6)  NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE order_items (
    id       BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    order_id BIGINT       NOT NULL,
    line_no  INT          NOT NULL,
    name     VARCHAR(255) NOT NULL,
    qty      INT          NOT NULL,
    price    BIGINT       NOT NULL, -- per unit, cents
    KEY idx_order_items_order (order_id),
    CONSTRAINT fk_order_items_order FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
