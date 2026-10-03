package io.github.trakka_project.app;

import android.Manifest;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.os.Build;
import android.os.Bundle;
import android.view.HapticFeedbackConstants;
import android.view.View;
import androidx.activity.EdgeToEdge;
import androidx.activity.SystemBarStyle;
import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import androidx.annotation.Nullable;
import androidx.appcompat.app.AppCompatActivity;
import androidx.camera.core.CameraSelector;
import androidx.camera.core.ImageAnalysis;
import androidx.camera.core.ImageProxy;
import androidx.camera.core.Preview;
import androidx.camera.lifecycle.ProcessCameraProvider;
import androidx.camera.view.PreviewView;
import androidx.core.content.ContextCompat;
import androidx.core.graphics.Insets;
import androidx.core.view.ViewCompat;
import androidx.core.view.WindowInsetsCompat;
import com.google.common.util.concurrent.ListenableFuture;
import com.google.zxing.BarcodeFormat;
import com.google.zxing.BinaryBitmap;
import com.google.zxing.DecodeHintType;
import com.google.zxing.MultiFormatReader;
import com.google.zxing.PlanarYUVLuminanceSource;
import com.google.zxing.ReaderException;
import com.google.zxing.common.HybridBinarizer;
import java.nio.ByteBuffer;
import java.util.Collections;
import java.util.EnumMap;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * Full-screen QR code scanner for the connect screen ({@link TrakkaAppPlugin#scanQrCode}): a CameraX
 * preview, with frames decoded on the device by ZXing. No network, no Google Play services. Returns
 * the code's text in {@link #EXTRA_TEXT}, or {@link #RESULT_CAMERA_DENIED} when the camera
 * permission is refused.
 */
public class QrScanActivity extends AppCompatActivity {

    static final String EXTRA_TEXT = "text";
    static final int RESULT_CAMERA_DENIED = RESULT_FIRST_USER;

    private final AtomicBoolean found = new AtomicBoolean();
    // Only used on analysisExecutor's single thread.
    private final MultiFormatReader reader = new MultiFormatReader();
    private ExecutorService analysisExecutor;
    private PreviewView preview;

    private final ActivityResultLauncher<String> cameraPermission = registerForActivityResult(
        new ActivityResultContracts.RequestPermission(),
        granted -> {
            if (granted) {
                startCamera();
            } else {
                finishWith(RESULT_CAMERA_DENIED, null);
            }
        }
    );

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        EdgeToEdge.enable(this, SystemBarStyle.dark(Color.TRANSPARENT), SystemBarStyle.dark(Color.TRANSPARENT));
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_qr_scan);
        preview = findViewById(R.id.qr_scan_preview);
        findViewById(R.id.qr_scan_close).setOnClickListener(view -> finishWith(RESULT_CANCELED, null));
        ViewCompat.setOnApplyWindowInsetsListener(findViewById(R.id.qr_scan_overlay), (view, insets) -> {
            Insets bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() | WindowInsetsCompat.Type.displayCutout());
            view.setPadding(bars.left, bars.top, bars.right, bars.bottom);
            return insets;
        });

        Map<DecodeHintType, Object> hints = new EnumMap<>(DecodeHintType.class);
        hints.put(DecodeHintType.POSSIBLE_FORMATS, Collections.singletonList(BarcodeFormat.QR_CODE));
        hints.put(DecodeHintType.ALSO_INVERTED, Boolean.TRUE);
        reader.setHints(hints);
        analysisExecutor = Executors.newSingleThreadExecutor();

        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
            startCamera();
        } else {
            cameraPermission.launch(Manifest.permission.CAMERA);
        }
    }

    @Override
    protected void onDestroy() {
        super.onDestroy();
        analysisExecutor.shutdown();
    }

    private void startCamera() {
        ListenableFuture<ProcessCameraProvider> future = ProcessCameraProvider.getInstance(this);
        future.addListener(
            () -> {
                try {
                    ProcessCameraProvider provider = future.get();
                    Preview previewUseCase = new Preview.Builder().build();
                    previewUseCase.setSurfaceProvider(preview.getSurfaceProvider());
                    ImageAnalysis analysis = new ImageAnalysis.Builder()
                        .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                        .build();
                    analysis.setAnalyzer(analysisExecutor, this::analyze);
                    CameraSelector camera = provider.hasCamera(CameraSelector.DEFAULT_BACK_CAMERA)
                        ? CameraSelector.DEFAULT_BACK_CAMERA
                        : CameraSelector.DEFAULT_FRONT_CAMERA;
                    provider.unbindAll();
                    provider.bindToLifecycle(this, camera, previewUseCase, analysis);
                } catch (Exception e) {
                    // No usable camera, or the camera service failed: nothing to scan with.
                    finishWith(RESULT_CANCELED, null);
                }
            },
            ContextCompat.getMainExecutor(this)
        );
    }

    private void analyze(ImageProxy image) {
        try {
            if (found.get()) {
                return;
            }
            // The Y plane of a YUV_420_888 frame is the luminance ZXing needs, one byte per pixel,
            // rows rowStride bytes apart.
            ImageProxy.PlaneProxy luminance = image.getPlanes()[0];
            ByteBuffer buffer = luminance.getBuffer();
            byte[] data = new byte[buffer.remaining()];
            buffer.get(data);
            PlanarYUVLuminanceSource source = new PlanarYUVLuminanceSource(
                data,
                luminance.getRowStride(),
                image.getHeight(),
                0,
                0,
                image.getWidth(),
                image.getHeight(),
                false
            );
            String text = reader.decodeWithState(new BinaryBitmap(new HybridBinarizer(source))).getText();
            if (found.compareAndSet(false, true)) {
                runOnUiThread(() -> {
                    preview.performHapticFeedback(
                        Build.VERSION.SDK_INT >= Build.VERSION_CODES.R
                            ? HapticFeedbackConstants.CONFIRM
                            : HapticFeedbackConstants.VIRTUAL_KEY
                    );
                    finishWith(RESULT_OK, text);
                });
            }
        } catch (ReaderException ignored) {
            // No QR code in this frame.
        } finally {
            reader.reset();
            image.close();
        }
    }

    private void finishWith(int resultCode, @Nullable String text) {
        setResult(resultCode, text == null ? null : new Intent().putExtra(EXTRA_TEXT, text));
        finish();
    }
}
