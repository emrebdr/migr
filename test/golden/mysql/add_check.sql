-- migr:statement 1
ALTER TABLE `orders` ADD CONSTRAINT `ck_orders_total` CHECK (total >= 0);
