package main

import (
	"flag"
	"testing"

	"k8s.io/klog"
)

func init() {
	klog.InitFlags(flag.CommandLine)
}

type fakeCommand struct {
	prefix  string
	command string
	args    []string
	err     error
}

func (f *fakeCommand) Command(index *Index, search string) (cmd string, args []string, err error) {
	return f.command, f.args, f.err
}
func (f *fakeCommand) PathPrefix() string {
	return f.prefix
}

func Test_executeGrepSingle(t *testing.T) {
	// cat, err := exec.LookPath("cat")
	// if err != nil {
	// 	t.Fatal(err)
	// }
	// cmd := &fakeCommand{prefix: "/var/lib/ci-search", command: cat, args: []string{"-e", "/tmp/logs"}}
	// fn := func(name string, search string, lines []bytes.Buffer, moreLines int) {
	// 	t.Logf("%s %d", name, len(lines))
	// }
	// if err := executeGrepSingle(context.TODO(), cmd, &Index{}, "etcdserver", 30, fn); err != io.EOF {
	// 	t.Fatal(err)
	// }
}
