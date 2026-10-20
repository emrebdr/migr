#!/bin/sh
# Lets the compose standby stream from this server (M13-T8). The image's own pg_hba.conf matches
# `all` databases, and a replication connection matches only the `replication` keyword.
set -e
echo "host replication all all scram-sha-256" >> "$PGDATA/pg_hba.conf"
