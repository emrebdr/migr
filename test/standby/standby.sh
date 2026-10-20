#!/bin/sh
# A PostgreSQL 16 streaming standby of the compose postgres16, applying WAL three seconds late
# (M13-T8). recovery_min_apply_delay holds back replay and not receipt, so the primary keeps no WAL
# for it and the delay is the lag pg_stat_replication reports for every write the tests make.
set -e

export PGDATA=/var/lib/postgresql/data

if [ ! -s "$PGDATA/PG_VERSION" ]; then
	until pg_isready -h postgres16 -U migr -d migr -q; do sleep 1; done
	rm -rf "${PGDATA:?}"/*
	pg_basebackup -h postgres16 -U migr -D "$PGDATA" -X stream -R
	chmod 0700 "$PGDATA"
	{
		echo "primary_conninfo = 'host=postgres16 port=5432 user=migr password=$PGPASSWORD application_name=standby16'"
		echo "recovery_min_apply_delay = '3s'"
	} >> "$PGDATA/postgresql.auto.conf"
fi

exec postgres
