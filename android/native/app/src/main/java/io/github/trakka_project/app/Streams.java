package io.github.trakka_project.app;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;

final class Streams {

    private Streams() {}

    /** Reads a UTF-8 text, refusing anything longer than {@code maxBytes}. */
    static String readUtf8(InputStream in, int maxBytes) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buffer = new byte[8192];
        for (int n; (n = in.read(buffer)) != -1; ) {
            if (out.size() + n > maxBytes) {
                throw new IOException("more than " + maxBytes + " bytes");
            }
            out.write(buffer, 0, n);
        }
        return out.toString("UTF-8");
    }
}
