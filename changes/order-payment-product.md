---
kind: fix
area: lifecycle
---
A payment counted from an Order Completed event now takes the product the order names, so a company's first order moves it to active on that product. A payment sent first without a product picks it up from its order.
