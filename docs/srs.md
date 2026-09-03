# Software Requirements Specification
## Group 4: Online Library Management System

**Course:** SEN 106 / SEN 216, Introduction to Web Technologies
**Institution:** Obafemi Awolowo University, Ile-Ife
**Modelled on:** Hezekiah Oluwasanmi Library (HOL)
**Document status:** Baseline, Phase 01
**Structure:** IEEE 830

---

## 1. Introduction

### 1.1 Purpose
This document specifies the requirements for a web-based library management system for
Hezekiah Oluwasanmi Library. It is the baseline against which the architecture, the
implementation and the tests are written and verified. Every unit of production code traces
back to an identifier in this document.

### 1.2 Scope
The system is a **modernised Online Public Access Catalogue (OPAC) with an integrated
circulation module**. It digitises two functions that HOL currently performs with a card
catalogue and a paper register at the Loans desk:

1. **Discovery**: answering "does the library have this book, is a copy free, and where is it?"
2. **Custody**: recording which registered member physically holds which copy, from when, and until when.

The system does **not** deliver book content. It is an inventory-and-custody system for
physical volumes, not a digital library.

### 1.3 Out of scope
Online reading, PDF or EPUB upload, ebook download, audiobooks, DRM, book sales,
purchasing, and online payments. These are excluded from the architecture, not merely
unimplemented. Fines are excluded as a consequence of excluding payments; overdue
items are surfaced for visibility only.

### 1.4 Definitions
| Term | Meaning |
|---|---|
| **Book** | A bibliographic record, one title, author, edition. Shared by all its copies. |
| **Copy** | One physical volume on a shelf, individually identified. |
| **Call number / class mark** | LCC code locating a title's subject, e.g. `DT 515.15 .Ob21`. Identical across all copies of a title. |
| **Accession number** | Number assigned to a physical item on arrival. **Unique to a single copy.** |
| **Loan** | A record binding a member to a copy, with borrow date, due date, return date and status. |
| **Member** | A person registered in person at the library and issued a library card. |
| **HOL** | Hezekiah Oluwasanmi Library. |
| **LCC** | Library of Congress Classification, the scheme in use at HOL. |

### 1.5 Sources of requirements
- The project brief issued to Group 4.
- **Document analysis** of the LIB 001 Library Instruction Programme course material, which
  describes HOL's actual organisation, classification, collections and access rules. Domain
  requirements (`DOM-*`) are drawn from it and cited.
- SEN 106 course learning outcomes, which define the assessed technical surface.

---

## 2. Overall description

### 2.1 Actors
| Actor | Description |
|---|---|
| **Member** | A registered student or staff member. Reads the catalogue and their own record. Cannot alter library data. |
| **Librarian** | Staff at the Circulation / Loans desk. Manages the catalogue, copies, members, and records custody events. |
| **Administrator** | Manages librarian accounts and system configuration. Superset of librarian. |

A member can never record their own borrowing. Custody events are recorded by staff at the
desk, exactly as they are today.

### 2.2 Member categories (DEC-004, DEC-005)
| Category | Max concurrent loans | Loan period |
|---|---|---|
| Undergraduate | 2 | 14 days |
| Postgraduate | 4 | 21 days |
| Staff | 6 | 28 days |

### 2.3 Copy loan policies (DEC-002)
| Policy | Borrowable | Basis in LIB 001 material |
|---|---|---|
| `circulating` | Yes | Main lending collection |
| `reference_only` | No | Reference collection is shelved in the Reference Room for consultation |
| `on_display` | No, but reservable | Recent Accessions "may not be borrowed while on display but may be reserved at the Loans desk" |
| `restricted` | No | Africana, OAU Publications, Conservation Room, Serials, request-form access |

### 2.4 Assumptions and constraints
- Backend in **Go**, containerised with Docker, deployed to Render behind Cloudflare.
- Frontend is a separate repository, built after the backend, consuming a documented HTTP API.
- The API is the only interface to the data. All business rules are enforced server-side.

---

## 3. Domain requirements

Drawn from the LIB 001 material. These describe how HOL actually works; violating one means
the system models a library that does not exist.

| ID | Requirement |
|---|---|
| DOM-001 | Classification shall use the **Library of Congress Classification** scheme, not Dewey. HOL uses LCC. |
| DOM-002 | A call number is shared by all copies of a title; an **accession number is unique to one physical copy**. Three copies of a title share a class number and hold three different accession numbers. |
| DOM-003 | Shelf location shall be derived from the call number's LCC letter: classes **A to J are in the South wing**, classes **K to Z are in the North wing**. |
| DOM-004 | Not every item circulates. Reference, serials, on-display and restricted collections shall not be borrowable. |
| DOM-005 | Borrowing privileges shall vary by member category. HOL grants postgraduates and senior staff access that undergraduates do not have. |
| DOM-006 | Membership shall originate in person. Users present a university identity card and library card and sign a register. The system shall not offer public self-registration. |
| DOM-007 | The catalogue shall provide **at least three access points, author, title and subject**: mirroring the card catalogue and HOL's OPAC, which searches "author, title and subject entries, keywords in titles". |
| DOM-008 | Borrowing history shall be preserved after return. A returned loan is closed, never deleted. |
| DOM-009 | Member identity and borrowing history are personal data under Nigerian data protection law. A borrowing history is a reading-habits profile and shall be treated as sensitive. |

---

## 4. Functional requirements

### 4.1 Authentication and accounts
| ID | Requirement |
|---|---|
| REQ-001 | A member shall log in with their matriculation/staff ID or university email, plus a password. |
| REQ-002 | The system shall not provide public self-registration. Accounts are created by staff only. (DOM-006) |
| REQ-003 | A user shall change their own password by supplying the current password. |
| REQ-004 | A user shall request a password reset, delivered as a link to their registered email. |
| REQ-005 | A password reset token shall be single-use and expire within 30 minutes. |
| REQ-006 | A user shall log out, invalidating their refresh token. |
| REQ-007 | A user created by staff shall be required to change their password at first login. |
| REQ-008 | The system shall assign every account exactly one role: `member`, `librarian` or `admin`. |

### 4.2 Member management (librarian)
| ID | Requirement |
|---|---|
| REQ-009 | A librarian shall create a member record with name, ID, email and category. |
| REQ-010 | A librarian shall bulk-import members from a CSV file. |
| REQ-011 | The CSV import shall report per-row success and failure without aborting the whole batch. |
| REQ-012 | A librarian shall search and list members. |
| REQ-013 | A librarian shall view a member's profile including current loans and full history. |
| REQ-014 | A librarian shall update a member record. |
| REQ-015 | A librarian shall suspend or reactivate a member. A suspended member cannot borrow. |

### 4.3 Catalogue
| ID | Requirement |
|---|---|
| REQ-016 | A librarian shall create a book record: title, author(s), ISBN, publisher, place and year of publication, LCC call number, subject headings, description. |
| REQ-017 | A librarian shall import bibliographic data from an external book API by ISBN to pre-fill a new record. (DEC-007) |
| REQ-018 | Imported data shall be editable before saving; the librarian's entry is authoritative. |
| REQ-019 | A librarian shall update a book record. |
| REQ-020 | A librarian shall archive a book. Archived books leave search results but their loan history is preserved. (DOM-008) |
| REQ-021 | A librarian shall manage subject headings and LCC categories. |

### 4.4 Copies and inventory
| ID | Requirement |
|---|---|
| REQ-022 | A librarian shall add a copy to a book, recording a **unique accession number**. (DOM-002) |
| REQ-023 | The system shall reject a duplicate accession number. |
| REQ-024 | Each copy shall carry a loan policy: `circulating`, `reference_only`, `on_display` or `restricted`. (DOM-004) |
| REQ-025 | Each copy shall carry a status: `available`, `on_loan`, `lost`, `damaged` or `withdrawn`. |
| REQ-026 | A librarian shall mark a copy lost, damaged or withdrawn, removing it from circulation without deleting its history. |
| REQ-027 | The system shall derive and display a copy's wing from its call number. (DOM-003) |

### 4.5 Search and discovery
| ID | Requirement |
|---|---|
| REQ-028 | A user shall search the catalogue by **title**. (DOM-007) |
| REQ-029 | A user shall search by **author**. (DOM-007) |
| REQ-030 | A user shall search by **subject**. (DOM-007) |
| REQ-031 | A user shall search by free-text keyword across title, author and subject. |
| REQ-032 | A user shall search by ISBN. |
| REQ-033 | A user shall search by call number. |
| REQ-034 | A user shall filter results by LCC category and by availability. |
| REQ-035 | Search results shall be paginated. |
| REQ-036 | A user shall view a book's detail page showing bibliographic data, copy counts, availability and shelf location. |
| REQ-037 | Search shall be available to unauthenticated visitors; personal data shall not be. |

### 4.6 Availability
| ID | Requirement |
|---|---|
| REQ-038 | The system shall report, per book, the total number of copies, the number available and the number on loan. |
| REQ-039 | Availability shall be **derived from copy states**, never stored as a standalone flag. |
| REQ-040 | Non-circulating copies shall be reported as held by the library but not available to borrow. (DOM-004) |

### 4.7 Borrowing
| ID | Requirement |
|---|---|
| REQ-041 | A librarian shall record a loan of a specific copy to a specific member. |
| REQ-042 | The system shall compute the due date from the member's category. (DEC-005) |
| REQ-043 | The system shall reject a loan that would exceed the member's category limit. |
| REQ-044 | The system shall reject a loan of a non-circulating copy. (DOM-004) |
| REQ-045 | The system shall reject a loan to a suspended or inactive member. |
| REQ-046 | The system shall reject a loan of a copy that is not `available`. |
| REQ-047 | Concurrent attempts to lend the same copy shall result in exactly one successful loan. |

### 4.8 Returns
| ID | Requirement |
|---|---|
| REQ-048 | A librarian shall record the return of a borrowed copy. |
| REQ-049 | Recording a return shall set the loan's return date and close its status. |
| REQ-050 | Recording a return shall restore the copy to `available`. |
| REQ-051 | The system shall reject a return for a loan that is already closed. |

### 4.9 Overdue
| ID | Requirement |
|---|---|
| REQ-052 | The system shall identify loans whose due date has passed and which are unreturned. |
| REQ-053 | Overdue status shall be **computed**, not stored, so it cannot go stale. |
| REQ-054 | A librarian shall list all overdue loans, filterable by member and by book. |

### 4.10 Reservations (DEC-003)
| ID | Requirement |
|---|---|
| REQ-055 | A member shall reserve a book whose copies are all on loan, or which is on display. |
| REQ-056 | Reservations shall be queued in order of request. |
| REQ-057 | A member shall view and cancel their own reservations. |
| REQ-058 | When a copy becomes available, the system shall notify the member at the head of the queue. |
| REQ-059 | An unclaimed reservation shall expire after a configurable period and pass to the next in queue. |

### 4.11 History
| ID | Requirement |
|---|---|
| REQ-060 | A member shall view the books they currently hold, with due dates. |
| REQ-061 | A member shall view their own complete borrowing history. |
| REQ-062 | A member shall not view any other member's history. (DOM-009) |
| REQ-063 | A librarian shall view the full loan history of any member or any copy. |
| REQ-064 | Loan records shall be immutable once closed. (DOM-008) |

### 4.12 Administration and reporting
| ID | Requirement |
|---|---|
| REQ-065 | A librarian shall view a dashboard: total books, total copies, active loans, overdue count, members. |
| REQ-066 | A librarian shall view most-borrowed titles over a period. |
| REQ-067 | An administrator shall create and manage librarian accounts. |
| REQ-068 | The system shall record an audit log of staff actions on catalogue, members and loans. |

### 4.13 Notifications (DEC-012)
| ID | Requirement |
|---|---|
| REQ-069 | The system shall notify a member by email before a loan falls due. |
| REQ-070 | The system shall notify a member when a loan becomes overdue. |
| REQ-071 | The system shall support push notification via Firebase Cloud Messaging. |
| REQ-072 | Notifications shall be queued and delivered asynchronously, never blocking an API response. |

### 4.14 API and operations
| ID | Requirement |
|---|---|
| REQ-073 | The system shall publish an OpenAPI 3 specification and serve interactive Swagger documentation. |
| REQ-074 | The system shall expose a health endpoint reporting service and database reachability. |

---

## 5. Non-functional requirements

| ID | Requirement | Measure |
|---|---|---|
| NFR-001 | Search response time | 95th percentile under 500 ms for a 10,000-book catalogue at 50 concurrent users |
| NFR-002 | Password storage | Argon2id, unique salt per password. Plaintext or unsalted hashes are a defect. |
| NFR-003 | Session tokens | JWT access token expiring in 15 minutes; refresh token in 7 days and revocable |
| NFR-004 | Access control | Role checked server-side on every protected endpoint. Client-side checks are not access control. |
| NFR-005 | Brute-force resistance | Authentication endpoints rate-limited to 5 attempts per minute per IP |
| NFR-006 | Transport | HTTPS only, HSTS enabled, HTTP redirected |
| NFR-007 | Input validation | Every request body and query parameter validated server-side before use |
| NFR-008 | Injection resistance | Parameterised queries exclusively. No string-concatenated SQL. |
| NFR-009 | Data integrity | `available_copies` shall never be negative and loans shall never exceed total copies. Enforced by database constraint and transaction, not application code alone. |
| NFR-010 | Logging | Structured logs. No passwords, tokens or member personal data written to logs. (DOM-009) |
| NFR-011 | Container size | Final Docker image under 50 MB via multi-stage build on a distroless or scratch base |
| NFR-012 | Test coverage | At least 70% statement coverage on business-logic packages |
| NFR-013 | Continuous integration | Every push runs build, vet, lint and the full test suite. A red build blocks merge. |
| NFR-014 | API documentation | Every endpoint documented in OpenAPI 3 with request, response and error shapes |
| NFR-015 | Secret management | All secrets supplied by environment variable. No credential committed to version control. |
| NFR-016 | CORS | Restricted to known frontend origins. Wildcard origins are a defect. |
| NFR-017 | Error handling | Uniform error envelope. Stack traces and driver errors never returned to clients. |
| NFR-018 | Portability | Runs from a single container image with no host-specific configuration |
| NFR-019 | Accessibility support | API supplies the text alternatives, labels and semantic data the frontend needs for WCAG compliance |
| NFR-020 | Auditability | Every state-changing staff action attributable to an account and a timestamp |

---

## 6. Validation

Each requirement above shall be traceable to at least one design element and at least one
test case before Gate G5. The traceability matrix at `.ilana/traceability.csv` is the
authoritative record.

## 7. Open items

| Item | Status |
|---|---|
| Renewals | Deferred to v2 (DEC-009) |
| Fines for overdue items | Out of scope, follows from excluding payments |
| Inter-library loan | Out of scope for v1 |
| Reservation expiry period | Configurable; default to be set at design time |
