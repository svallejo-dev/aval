# ✗ aval: block · tier 1 · enforce

**repo** `svallejo-dev/<script>alert("xss")</script><img src=x onerror=alert(1)>` · **trust base** `111111111111` · **change base** `444444444444` (release/\*\*bold\*\* \[link\](http:&#47;&#47;…) → **head** `222222222222` · **aval** `v0.1.0 red` · **generated** `2026-09-24T12:00:00Z`

**changes** `add-col | umn || more | cells`, ``**bold** [link](http://evil.example) `code` ~~strike~~ _em_ #heading``

## Reasons (2)

### Blocking (2)

- `unverified` — an added or modified obligation with no test bound to it
  - `ORD-N02`: col | umn || more | cells &lt;script&gt;alert("xss")&lt;/script&gt;&lt;img src=x onerror=alert(1)&gt; see https:&#47;&#47;evil.example/override, www&#46;evil.example or ops&#64;evil.example
- ``code_**bold** [link](http://evil.example) `code` ~~strike~~ _em_ #heading`` — a reason code this version of aval does not know, which blocks
  - `ORD-F01`: start gnidne isolate zero width soft

## Obligations (2)

| ID | kind | delta | bound tests | before → after | strength | note |
| --- | --- | --- | --- | --- | --- | --- |
| `ORD-F01` | F must | added | `TestX/ORD-F01_col \| umn \|\| more \| cells`, ````TestX/ORD-F01_a ``` b `` c ` d````, `TestX/longidentifierlongidentifierlongidentifierlongidentifierlongidentifierlon…` | fail → pass | strong | \*\*bold\*\* \[link\](http:&#47;&#47;evil.example) \`code\` \~\~strike\~\~ \_em\_ #heading col \| umn \|\| more \| cells see https:&#47;&#47;evil.example/override, www&#46;evil.example or ops&#64;evil.example 平文 école テスト &lt;script&gt;alert("xss… |
| `ORD-N02` | N must-not | added | `longidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongident…` | pass → pass | none | longidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongidentifierlon… |

## Tamper (1)

| signal | ID | detail |
| --- | --- | --- |
| fingerprint_changed | `ORD-F01` | fingerprint of \*\*bold\*\* \[link\](http:&#47;&#47;evil.example) \`code\` \~\~strike\~\~ \_em\_ #heading col \| umn \|\| more \| cells changed |

## Scope

1 commit in the range: 1 mixed, 0 seam. Only those are listed.

| commit | family | paths |
| --- | --- | --- |
| `222222222222` | mixed | `col \| umn \|\| more \| cells`, `<script>alert("xss")</script><img src=x onerror=alert(1)>`, `start gnidne isolate zero width soft`, `longidentifierlongidentifierlongidentifierlongidentifierlongidentifierlongident…` (+2 more) |

## Approvals (1)

| kind | actor | commit | valid | reason | rejection |
| --- | --- | --- | --- | --- | --- |
| override | `@<script>alert("xss")</script><img src=x onerror=alert(1)>` | `333333333333` (not head) | no | aval:override \*\*bold\*\* \[link\](http:&#47;&#47;evil.example) \`code\` \~\~strike\~\~ \_em\_ #heading col \| umn \|\| more \| cells see https:&#47;&#47;evil.example/override, www&#46;evil.example or ops&#64;evil.example | review of an earlier commit red |

## Checks (1)

| check | status | exit | duration | command | artifact |
| --- | --- | --- | --- | --- | --- |
| `go-test red` | not_run | -1 | 0ms | ````go test col \| umn \|\| more \| cells a ``` b `` c ` d```` | `<script>alert("xss")</script><img src=x onerror=alert(1)>` |

## Not collected (2)

`mutation col | umn || more | cells`, `<script>alert("xss")</script><img src=x onerror=alert(1)>`

## Reproduce

```sh
trust=1111111111111111111111111111111111111111
change=4444444444444444444444444444444444444444
head=2222222222222222222222222222222222222222
git fetch origin && git checkout "$head"
aval verify --trust-base "$trust" --change-base "$change" --head "$head"
aval gate --trust-base "$trust" --change-base "$change" --head "$head"
```
