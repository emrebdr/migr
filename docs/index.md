---
layout: home
title: migr

hero:
  name: migr
  text: Schema migrations as code
  tagline: Write them in Go, TypeScript or SQL. Run them on PostgreSQL, MySQL, MariaDB or SQLite. Safe on a live database.
  actions:
    - theme: brand
      text: Get started
      link: /getting-started
    - theme: alt
      text: The zero-downtime guide
      link: /zero-downtime

features:
  - title: Any database
    details: A migration says what changes, never how one engine spells it. Each driver compiles it for its server and declares what it can't do before anything runs.
  - title: Any language
    details: Go and TypeScript migrations emit the same operations byte for byte, checked by a shared conformance suite. Plain SQL works too, as an escape hatch.
  - title: Safe on a live database
    details: Every operation carries its lock cost. Statements wait under a timeout and retry with backoff, and a large table can be changed through a shadow copy that you cut over when you're ready.
---
