---
kind: improvement
area: analytics
---
Products add themselves from your events: an event that names a product and its category (`product` and `product_category`) adds the product with that category the first time it is seen, and keeps the category in step with later events. Events without a category are held for review as before.
