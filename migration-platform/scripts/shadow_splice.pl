#!/usr/bin/perl
# Static byte-splice worker for the OFFLINE shadow-rewrite harness.
#
# This mirrors the (future) remote worker but runs LOCALLY on fixtures only:
# there is no SSH, no real shadow, no cPanel. It exists so the Python harness
# can exercise the exact transport/splice contract offline.
#
# Contract:
#   argv  = <shadow_path> <username> <min_fields> <allow_empty:0|1>   (NO hashes)
#   stdin = line 1: expected_current  (field-2 CAS value)
#           line 2: new_value         (replacement for field 2)
#
# The hashes travel ONLY on stdin — never in argv or the environment.
# The script emits ONLY status tokens ("OK <basename>" / "ERR <code>") and
# never prints any hash, shadow line, or full path.
#
# It byte-splices EXCLUSIVELY field 2 of the single matching line (exact
# field-1 equality), preserving every other byte including the file's
# final-newline state, and writes the result to an O_EXCL temp in the same
# directory. Field replacement uses substr() on byte offsets (no regex
# interpolation, no awk print), so a value containing '$' is inert.
use strict;
use warnings;
use File::Temp qw(mkstemp);
use File::Basename qw(dirname basename);

my ($path, $user, $min, $allow_empty) = @ARGV;
unless (defined $path && defined $user && defined $min) { print "ERR args\n"; exit 2; }
$allow_empty = 0 unless defined $allow_empty;

# Structured stdin: exactly two lines.
my $expected = <STDIN>;
my $new      = <STDIN>;
unless (defined $expected && defined $new) { print "ERR stdin\n"; exit 2; }
chomp $expected;
chomp $new;

# Defensive: a replacement that could break the record structure is refused.
# CR is rejected too, for parity with the file-encoding guard / live barrier.
if ($new =~ /[:\n\r\x00]/) { print "ERR reject_new\n"; exit 7; }

open(my $fh, '<:raw', $path) or do { print "ERR open\n"; exit 3; };
local $/;
my $data = <$fh>;
close $fh;
$data = '' unless defined $data;
if (length($data) == 0) { print "ERR empty_file\n"; exit 3; }

# Fail-closed on unexpected control bytes / encoding.
if ($data =~ /[\r\x00]/) { print "ERR encoding\n"; exit 6; }

# Count exact field-1 matches; capture field-2 byte offsets of the (single) hit.
my $seen = 0;
my ($f2s, $f2e, $ls);
while ($data =~ /^(\Q$user\E:)([^:\n]*)(?=:|\n|$)/mg) {
    $seen++;
    $ls  = $-[0];
    $f2s = $-[2];
    $f2e = $+[2];
}
if ($seen != 1) { print "ERR seen $seen\n"; exit 4; }

# NF check on the full target line.
my $lend = index($data, "\n", $ls);
$lend = length($data) if $lend < 0;
my $line = substr($data, $ls, $lend - $ls);
my @fields = split(/:/, $line, -1);
if (scalar(@fields) < $min) { print "ERR nf " . scalar(@fields) . "\n"; exit 5; }

# CAS on field 2.
my $cur = substr($data, $f2s, $f2e - $f2s);
if ($cur ne $expected) { print "ERR cas\n"; exit 8; }

# Empty field 2 is a no-auth -> auth transition: require explicit confirmation.
if ($cur eq '' && !$allow_empty) { print "ERR empty_needs_confirm\n"; exit 9; }

# Pure byte-splice of ONLY field 2.
substr($data, $f2s, $f2e - $f2s) = $new;

# O_EXCL temp in the SAME directory (File::Temp mkstemp uses O_CREAT|O_EXCL, 0600).
my $dir = dirname($path);
my ($tfh, $tpath) = mkstemp("$dir/.shadow.mig.XXXXXX");
binmode $tfh;
print {$tfh} $data;
unless (close $tfh) { unlink $tpath; print "ERR write\n"; exit 10; }
chmod 0600, $tpath;

print "OK " . basename($tpath) . "\n";
exit 0;
