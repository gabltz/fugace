# Drives irssi through the conformance scenario (see run.sh). Event-driven so
# that irssi's own command queue cannot reorder the steps.
use strict; use Irssi;
my @cmds = ("MSG #general hello from irssi", "MSG observer private from irssi", "NICK irs2",
  "MSG observer after nick change", "ACTION #general waves", "TOPIC #general set by irssi",
  "PART #general bye");
my ($joins, $i, $timer) = (0, 0, undef);
sub server { my ($s) = Irssi::servers(); return $s; }
sub step {
  my $s = server() or return;
  my $c = $cmds[$i++];
  if (defined $c) { $s->command($c); } else { Irssi::timeout_remove($timer); }
}
Irssi::signal_add_last('channel joined', sub {
  $joins++;
  if ($joins == 1) { $timer = Irssi::timeout_add(2500, \&step, undef); }
  else { Irssi::timeout_add_once(2000, sub { my $s = server(); $s->command("QUOTE QUIT :leaving") if $s; }, undef); }
});
Irssi::signal_add_last('channel destroyed', sub {
  Irssi::timeout_add_once(1000, sub { my $s = server(); $s->command("JOIN #general") if $s; }, undef);
});
