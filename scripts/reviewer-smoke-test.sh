#!/usr/bin/env bash
# Throwaway file for a reviewer smoke test; this PR is closed unmerged.
for f in $(ls /tmp/*.log); do
  rm $f
done
