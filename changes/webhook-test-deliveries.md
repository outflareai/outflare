---
kind: improvement
area: api
---
Test events you send to a webhook are marked "Test" in its delivery list, with the receiver's status code, so you can show that the endpoint works. A failed test no longer counts against the endpoint. It doesn't start a failure streak, raise the error rate or turn the endpoint off on a 410.
