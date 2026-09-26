# The Go core is called through JNI by class and method name; shrinking must not rename or drop it.
-keep class go.** { *; }
-keep class mobile.** { *; }
-dontwarn go.**
