use strict; use Irssi;
my @cmds = ("MSG #general hello from irssi", "MSG observer private from irssi", "NICK irs2",
  "MSG observer after nick change", "ACTION #general waves", "TOPIC #general set by irssi",
  "PART #general bye", "JOIN #general", "QUIT leaving");
my ($started, $i) = (0, 0);
sub step { my ($s) = Irssi::servers(); return unless $s; my $c = $cmds[$i++]; $s->command($c) if defined $c; }
Irssi::signal_add_last('channel joined', sub { Irssi::timeout_add(2500, \&step, undef) unless $started++; });
