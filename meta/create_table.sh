#!/bin/sh

# Script to initialize HBase tables for YIG
# Refer to http://wiki.letv.cn/display/pla/HBase+Table+Schema for more information

exec hbase shell <<EOF
create 'buckets',
  {NAME => 'b', VERSIONS => 1}

create 'objects',
  {NAME => 'o', VERSIONS => 1},
  {NAME => 'p', VERSIONS => 1}

create 'users',
  {NAME => 'o', VERSIONS => 1}

create 'multiparts',
  {NAME => 'm', VERSIONS => 1}

create 'garbageCollection',
  {NAME => 'gc', VERSIONS => 1}
EOF