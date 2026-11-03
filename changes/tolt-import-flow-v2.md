---
kind: fix
area: partners
---
The Tolt import now reads commission flows as Tolt's API sends them, so a program's flows come over with their conditions, approval and partner overrides. Tolt's "number of customer transactions" counts the payment itself, and the import adjusts for that, so a "first payment" flow still pays only on the first payment. Paused flows stay behind.
