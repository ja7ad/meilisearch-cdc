CREATE TABLE products (
  id          bigint PRIMARY KEY,
  name        text NOT NULL,
  description text,
  category    text,
  price       numeric(10,2),
  in_stock    boolean DEFAULT true,
  lat         double precision,
  lng         double precision,
  updated_at  timestamptz DEFAULT now(),
  deleted_at  timestamptz
);
INSERT INTO products (id, name, description, category, price, lat, lng)
SELECT g, 'Product ' || g, 'A great product number ' || g,
       (ARRAY['books','games','music','garden'])[1 + g % 4],
       (g % 1000) / 10.0, 48.8 + (g % 100) / 1000.0, 2.35 + (g % 100) / 1000.0
FROM generate_series(1, 10000) g;
