# jstack2devtools
Converts java stacktraces to Chromium DevTools format .json
Similar to VisualVM and JProfiler, but better - contains Timeline

![alt text](https://github.com/Szperak/jstack2devtools/raw/master/demo.png "Chrome demo")

Building
====
`go build github.com/Szperak/jstack2devtools`

Running
=====
Paste your stacks.txt to the workdir
Run `./jstack2devtools` and import `events.json` in your DevTools





Generating smoother samples than `jstack`
======

```
String threadName = "Client thread";
Thread jt = getThreadByName(threadName);
ByteArrayOutputStream baos = new ByteArrayOutputStream();
long lastTime = System.nanoTime();
for(int i = 0; i<20000; i++) {
    baos.write('"');
    baos.write(jt.getName().getBytes(StandardCharsets.UTF_8));
    baos.write('"');
    baos.write('\n');
    
    StackTraceElement[] stack = jt.getStackTrace();
    for(StackTraceElement el : stack) {
        baos.write("\tat ".getBytes(StandardCharsets.UTF_8));
        
        String stackElement = "";
        if(el != null){
        	stackElement = el.getClassName()+"."+el.getMethodName()+"("+el.getFileName()+":"+el.getLineNumber()+")";
        }
        baos.write(stackElement.getBytes(StandardCharsets.UTF_8));
        baos.write('\n');
        
    }
    long now = System.nanoTime();
    baos.write(Long.toString(now-lastTime).getBytes(StandardCharsets.UTF_8));
    baos.write('n');
    baos.write('s');
    baos.write('\n');
    lastTime = now;
    Thread.sleep(0, 1000*500);
}
String fileName = "stacks.txt";
FileOutputStream fos = new FileOutputStream(fileName);
fos.write(baos.toByteArray());
fos.close();
```
