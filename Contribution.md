# Contributing to Magnetk

First, thank you for taking the time to read this. Magnetk is an ambitious project and every serious contributor makes a real difference.

This document explains how to get involved, what kind of contributions are needed right now, and how the process works.

---

## Before You Start

Read these two documents first. They are short and will save you a lot of time:

- [MagnetkCore.md](./docs/MAGNETK_CORE.md) — what Magnetk is, the full architecture, and why each layer exists
- [ROADMAP.md](./docs/ROADMAP.md) — the development roadmap, phase by phase, in order

Understanding the architecture before writing code means your contribution fits the design rather than working against it.

---

## What We Need Right Now

The project is currently in **Phase 1** of the roadmap. Contributions outside Phase 1 will not be merged yet — not because they are bad, but because later phases depend on Phase 1 being solid first.

### Phase 1 priorities — open for contribution now

**Multi-seeder parallel download**
The single most impactful improvement available. Right now one seeder serves one downloader. The upgrade: chunks are downloaded simultaneously from multiple seeders, each assigned different ranges. If you understand BitTorrent's rarest-first chunk selection or have experience with parallel download scheduling, this is where to start.

**DHT peer discovery**
Replace the central relay as the peer discovery mechanism with a proper Kademlia DHT using LibP2P. Peers should find each other by content hash without any central server involved. LibP2P already has Kademlia support — the work is integration and testing.

**NAT hole punching**
STUN-based hole punching for peers behind NAT so they can connect directly without the relay. The relay stays as a last resort for symmetric NAT and CGNAT, but most consumer NAT should be punched through directly. Reduces relay load significantly.

**Adaptive chunk sizing**
Chunk size currently fixed. Should adapt per-peer based on measured round-trip time and available bandwidth. Bigger chunks on fast stable connections, smaller on slow or lossy ones.

**Transport hardening**
Edge case fixes, retry logic, connection drop handling, and test coverage for the existing Go, JS, and Python SDKs. If you find a bug, a crash, or a scenario where transfer fails silently — that is a valid and valuable contribution.

### Good first issues

If you are new to the codebase, look for issues tagged `good first issue` on the GitHub Issues page. These are scoped tasks that do not require deep knowledge of the full architecture. Good starting points include:

- Adding test coverage for existing SDK methods
- Improving error messages to be more descriptive
- Documenting undocumented functions in any SDK
- Fixing a specific reported bug

---

## How to Contribute

### 1. Open an issue before starting large work

If you want to work on something significant — a new feature, a refactor, a new SDK — open an issue first and describe what you plan to do. This avoids two people working on the same thing and avoids building something that conflicts with the roadmap direction.

For small fixes (typos, minor bugs, test additions) you can skip this and go straight to a pull request.

### 2. Fork and clone

```bash
git clone https://github.com/iMarand/magnetk.git
cd magnetk
```

Work on a branch named for what you are doing:

```bash
git checkout -b feature/multi-seeder-download
git checkout -b fix/chunk-retry-on-drop
git checkout -b docs/contributing-guide
```

### 3. Build and verify

Make sure the project builds before making changes and after:

**Linux / macOS**
```bash
chmod +x scripts/build.sh
./scripts/build.sh
```

**Windows (PowerShell)**
```powershell
$env:Path = "C:\msys64\mingw64\bin;" + $env:Path
./scripts/build.bat
```

If the build breaks after your changes, fix it before submitting.

### 4. Write tests for what you build

New features need tests. Bug fixes need a test that would have caught the bug. We do not have a coverage target number — the rule is simpler: if someone could break your code without a test failing, add a test.

### 5. Submit a pull request

Keep pull requests focused. One PR should do one thing. A PR that adds multi-seeder download, fixes a relay bug, and updates the README is three PRs, not one.

In your PR description, include:

- What the change does
- Why it is needed
- How you tested it
- Any known limitations or follow-up work

---

## Code Standards

**Go**
- Follow standard Go formatting (`gofmt` before committing)
- Exported functions must have comments
- Errors must be returned and handled, never silently swallowed
- Avoid global state

**JavaScript**
- ES modules, not CommonJS
- Async/await over raw promises
- No dependencies added without discussion first

**C++**
- C++17 minimum
- RAII for resource management
- No raw `new`/`delete` — use smart pointers

**All languages**
- No commented-out code in PRs
- No debug print statements left in
- Variable names that say what something is, not what type it is

---

## What We Will Not Merge

- Code that breaks the existing build
- Features from Phase 2 or later while Phase 1 is incomplete
- Dependencies added without prior discussion
- Code that has no tests and touches core transport logic
- Whitespace-only or formatting-only PRs on files you did not otherwise touch

---

## Architecture Decisions

If you think something in the core architecture is wrong or could be better, open an issue and make the case. Architecture discussions are welcome. Unilateral architecture changes in a PR are not — they will be closed and redirected to a discussion.

---

## Reporting Bugs

Open a GitHub Issue with:

- What you were doing
- What you expected to happen
- What actually happened
- Your OS, Go version, and SDK version
- Minimal reproduction steps if possible

The more specific, the faster it gets fixed.

---

## Reporting Security Issues

Do not open a public GitHub Issue for security vulnerabilities. Email directly at the address in the GitHub profile. Include a description of the vulnerability and steps to reproduce. We will respond within 72 hours and work with you on disclosure timing.

---

## Questions

If you are unsure whether something is in scope, unsure how part of the codebase works, or unsure whether your approach is the right one — open a GitHub Discussion or comment on a relevant Issue. No question is too basic. A question asked saves days of work going in the wrong direction.

---

## Recognition

Contributors are listed in the project changelog with their GitHub handle and a description of what they contributed. Significant contributors — people who ship a full Phase 1 feature — will be listed in the README.

