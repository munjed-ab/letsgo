package com.letsgo.app

import android.media.AudioFormat
import android.media.MediaCodec
import android.media.MediaExtractor
import android.media.MediaFormat
import mobile.VideoDecoder
import java.io.IOException
import java.nio.ByteBuffer
import java.nio.ByteOrder

/**
 * The sound of a video file, decoded by Android's own codecs and handed to the node as 16-bit stereo PCM,
 * the way its Go decoders hand it an mp3. The node plays one file at a time and calls in from one thread.
 */
class VideoAudio : VideoDecoder {
    private var extractor: MediaExtractor? = null
    private var codec: MediaCodec? = null
    private var hz = 44100
    private var channels = 2
    private var isFloat = false
    private var picture = true // the file has a video track
    private var inputDone = false
    private var outputDone = false
    private var skipUs = 0L // after a seek: drop decoded audio that comes before this position
    private var first: ByteArray? = null // decoded while opening, see open()
    private val info = MediaCodec.BufferInfo()

    override fun open(path: String): Long {
        close()
        val ex = MediaExtractor()
        extractor = ex
        try {
            ex.setDataSource(path)
            picture = (0 until ex.trackCount).any { ex.getTrackFormat(it).getString(MediaFormat.KEY_MIME)?.startsWith("video/") == true }
            val track = (0 until ex.trackCount).firstOrNull { ex.getTrackFormat(it).getString(MediaFormat.KEY_MIME)?.startsWith("audio/") == true }
                ?: throw IOException("no audio track")
            ex.selectTrack(track)
            val fmt = ex.getTrackFormat(track)
            val c = MediaCodec.createDecoderByType(fmt.getString(MediaFormat.KEY_MIME)!!)
            codec = c
            c.configure(fmt, null, null, 0)
            c.start()
            hz = fmt.getInteger(MediaFormat.KEY_SAMPLE_RATE)
            channels = fmt.getInteger(MediaFormat.KEY_CHANNEL_COUNT)
            inputDone = false
            outputDone = false
            skipUs = 0
            // What the decoder really outputs (HE-AAC doubles the rate the file states) is only known
            // once it has produced something, and the node needs the rate now: decode a block ahead.
            first = decode()
            return if (fmt.containsKey(MediaFormat.KEY_DURATION)) fmt.getLong(MediaFormat.KEY_DURATION) else 0
        } catch (e: Exception) {
            close()
            throw e
        }
    }

    override fun rate(): Long = hz.toLong()

    override fun noPicture(): Boolean = !picture

    override fun read(): ByteArray {
        first?.let { first = null; return it }
        return decode()
    }

    override fun seekUs(us: Long) {
        val ex = extractor ?: return
        val c = codec ?: return
        first = null
        ex.seekTo(us, MediaExtractor.SEEK_TO_PREVIOUS_SYNC)
        c.flush()
        inputDone = false
        outputDone = false
        skipUs = us
    }

    override fun close() {
        first = null
        try { codec?.release() } catch (_: Exception) {}
        codec = null
        try { extractor?.release() } catch (_: Exception) {}
        extractor = null
    }

    /** The next block of decoded audio; empty at the end of the track. */
    private fun decode(): ByteArray {
        val c = codec ?: return EMPTY
        val ex = extractor ?: return EMPTY
        var idle = 0
        while (!outputDone) {
            if (!inputDone) {
                val i = c.dequeueInputBuffer(0)
                if (i >= 0) {
                    val n = ex.readSampleData(c.getInputBuffer(i)!!, 0)
                    if (n < 0) {
                        c.queueInputBuffer(i, 0, 0, 0, MediaCodec.BUFFER_FLAG_END_OF_STREAM)
                        inputDone = true
                    } else {
                        c.queueInputBuffer(i, 0, n, ex.sampleTime, 0)
                        ex.advance()
                    }
                }
            }
            val o = c.dequeueOutputBuffer(info, 5_000)
            if (o == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                readFormat(c.outputFormat)
                continue
            }
            if (o < 0) {
                if (++idle > 1000) throw IOException("the decoder stopped answering")
                continue
            }
            idle = 0
            val pcm = toStereo16(c.getOutputBuffer(o)!!, info)
            if (info.flags and MediaCodec.BUFFER_FLAG_END_OF_STREAM != 0) outputDone = true
            c.releaseOutputBuffer(o, false)
            if (pcm.isNotEmpty()) return pcm
        }
        return EMPTY
    }

    private fun readFormat(f: MediaFormat) {
        hz = f.getInteger(MediaFormat.KEY_SAMPLE_RATE)
        channels = f.getInteger(MediaFormat.KEY_CHANNEL_COUNT)
        isFloat = f.containsKey(MediaFormat.KEY_PCM_ENCODING) && f.getInteger(MediaFormat.KEY_PCM_ENCODING) == AudioFormat.ENCODING_PCM_FLOAT
    }

    /** One decoded buffer as 16-bit little-endian stereo, minus what a seek asked to skip. 5.1 is folded down. */
    private fun toStereo16(buf: ByteBuffer, bi: MediaCodec.BufferInfo): ByteArray {
        buf.order(ByteOrder.LITTLE_ENDIAN)
        val width = if (isFloat) 4 else 2
        val frames = bi.size / (width * channels)
        var skip = 0
        if (skipUs > 0) {
            if (bi.presentationTimeUs + frames * 1_000_000L / hz <= skipUs) return EMPTY // all before the seek target
            skip = ((skipUs - bi.presentationTimeUs) * hz / 1_000_000L).toInt().coerceIn(0, frames)
            skipUs = 0
        }
        val out = ByteBuffer.allocate((frames - skip) * 4).order(ByteOrder.LITTLE_ENDIAN)
        val s = FloatArray(channels)
        for (f in skip until frames) {
            for (k in 0 until channels) {
                val at = bi.offset + (f * channels + k) * width
                s[k] = if (isFloat) buf.getFloat(at) else buf.getShort(at) / 32768f
            }
            var l = s[0]
            var r = if (channels > 1) s[1] else s[0]
            if (channels >= 6) { // L R C LFE Ls Rs: the centre (dialogue) and the surrounds go into both sides
                l = (s[0] + 0.707f * s[2] + 0.707f * s[4]) / 2.414f
                r = (s[1] + 0.707f * s[2] + 0.707f * s[5]) / 2.414f
            }
            out.putShort(pcm16(l))
            out.putShort(pcm16(r))
        }
        return out.array()
    }

    private fun pcm16(v: Float): Short = (v * 32767f).coerceIn(-32768f, 32767f).toInt().toShort()

    private companion object {
        val EMPTY = ByteArray(0)
    }
}
