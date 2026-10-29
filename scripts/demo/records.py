"""Adds Northpeak's customer records to the demo workspace, through the API.

People, companies, their emails and ids, field changes, consent, product
events and two possible duplicates, so the People page has something to
show. Everyone here is fictional (.example).

Usage: python3 records.py WORKSPACE_ID TOKEN [BASE_URL]
TOKEN is an API token with customers.read and customers.manage.
Prints the ids the screenshots open to stderr.
"""
import json
import sys
import urllib.request

ws, token = sys.argv[1], sys.argv[2]
base = (sys.argv[3] if len(sys.argv) > 3 else "http://localhost:8080") + f"/v1/workspaces/{ws}"


def call(method, path, body=None):
    req = urllib.request.Request(base + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"})
    with urllib.request.urlopen(req) as r:
        return json.loads(r.read() or b"{}")


companies = {}
for name, domain, industry, size, stage in [
    ("Acme Analytics", "acme-analytics.example", "software", "51_200", "customer"),
    ("Brightline Logistics", "brightline.example", "logistics", "201_500", "trial"),
    ("Cobalt Health", "cobalthealth.example", "healthcare", "501_1000", "lead"),
    ("Driftwood Studio", "driftwood.example", "agency", "11_50", "customer"),
    ("Evergreen Pay", "evergreenpay.example", "fintech", "1001_5000", "prospect"),
]:
    c = call("POST", "/records", {"type": "account", "name": name, "origin": "first_party" if stage in ("customer", "trial") else "prospect",
                                  "attributes": {"website": f"https://{domain}", "industry": industry, "company_size": size, "lifecycle_stage": stage, "country": "US"},
                                  "identities": [{"namespace": "domain", "value": domain}]})
    companies[domain] = c["id"]

people = {}
for first, last, title, seniority, domain, stage, origin in [
    ("Ana", "Diaz", "Head of Growth", "head", "acme-analytics.example", "customer", "first_party"),
    ("Ben", "Okafor", "CTO", "c_suite", "acme-analytics.example", "customer", "first_party"),
    ("Chloe", "Martin", "Operations Manager", "manager", "brightline.example", "trial", "first_party"),
    ("Dev", "Patel", "VP Marketing", "vp", "cobalthealth.example", "lead", "prospect"),
    ("Elif", "Yilmaz", "Founder", "owner", "driftwood.example", "customer", "first_party"),
    ("Femi", "Adeyemi", "Director of Finance", "director", "evergreenpay.example", "prospect", "prospect"),
    ("Grace", "Liu", "Product Designer", "senior", "driftwood.example", "signed_up", "first_party"),
    ("Hugo", "Laurent", "Growth Marketer", "senior", "brightline.example", "lead", "prospect"),
]:
    email = f"{first.lower()}@{domain}"
    ids = [{"namespace": "email", "value": email, "verified": origin == "first_party"}]
    if origin == "first_party":
        ids.append({"namespace": "user_id", "value": f"usr_{first.lower()}"})
    p = call("POST", "/records", {"type": "person", "origin": origin, "acquired_via": "apollo" if origin == "prospect" else "",
                                  "attributes": {"first_name": first, "last_name": last, "job_title": title, "seniority": seniority,
                                                 "companies": [companies[domain]], "lifecycle_stage": stage, "country": "US"},
                                  "identities": ids})
    people[first] = p

# History: Ana was promoted and moved from trial to customer.
ana = people["Ana"]
call("PATCH", f"/records/{ana['id']}", {"attributes": {"job_title": "VP Growth", "seniority": "vp"}})
call("POST", f"/records/{ana['id']}/consents", {"channel": "email", "status": "granted", "source": "signup_form"})
call("POST", f"/records/{ana['id']}/consents", {"channel": "email", "topic": "product_updates", "status": "granted", "source": "preference_center"})
call("POST", f"/records/{people['Grace']['id']}/consents", {"channel": "email", "status": "withdrawn", "source": "unsubscribe_link"})

# What they did in the product.
call("POST", "/customer-events", {"batch": [
    {"type": "track", "userId": "usr_ana", "event": "Report Created", "properties": {"report": "weekly_pipeline"}},
    {"type": "track", "userId": "usr_ana", "event": "Teammate Invited", "properties": {"seats": 3}},
    {"type": "track", "userId": "usr_ben", "event": "Integration Connected", "properties": {"integration": "hubspot"}},
    {"type": "track", "userId": "usr_chloe", "event": "Trial Started", "properties": {"plan": "growth"}},
]})

# Two possible duplicates: the same people again from a CRM import, with
# the same emails unverified.
for who in ("Ana", "Chloe"):
    p = people[who]
    email = next(i["value"] for i in p["identities"] if i["namespace"] == "email")
    call("POST", "/records", {"type": "person", "origin": "crm_sync", "name": p["name"],
                              "attributes": {"job_title": "Growth" if who == "Ana" else "Ops"},
                              "identities": [{"namespace": "email", "value": email}]})

print(json.dumps({"person": ana["id"], "company": companies["acme-analytics.example"]}), file=sys.stderr)
