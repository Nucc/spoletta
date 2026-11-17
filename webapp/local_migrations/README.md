# Local migrations

A webapp built on Spoletta's code that needs tables or columns of its own
puts the migrations making them here, numbered from 1, apart from
Spoletta's in [../migrations](../migrations):

```
00001_add_user_badges.sql
00002_add_badge_colours.sql
```

They run after Spoletta's, each once, recorded in `goose_local_version`,
and are written and undone as Spoletta's are, with `webapp migrate local
down-to <version>`. Spoletta ships this directory with only this file, so
add files beside it and leave it as it is: Spoletta's releases then merge
without touching what you added.

[docs/MIGRATIONS.md](../../docs/MIGRATIONS.md#local-migrations) says more.
