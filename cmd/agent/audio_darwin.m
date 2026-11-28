// System sound on macOS: ScreenCaptureKit audio capture (macOS 13+).
// It's covered by the Screen Recording permission HopDesk already has.
// Manual reference counting (no ARC), so the package's other C code builds unchanged.

#import <Foundation/Foundation.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreMedia/CoreMedia.h>
#import <AudioToolbox/AudioToolbox.h>
#include <stdlib.h>
#include <string.h>
#include "_cgo_export.h"

@interface HDAudioOutput : NSObject <SCStreamOutput, SCStreamDelegate>
@property (nonatomic) int token;
@end

@implementation HDAudioOutput

- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeAudio || !CMSampleBufferIsValid(sb)) return; // screen frames: ignored
    CMFormatDescriptionRef fd = CMSampleBufferGetFormatDescription(sb);
    const AudioStreamBasicDescription *asbd = fd ? CMAudioFormatDescriptionGetStreamBasicDescription(fd) : NULL;
    if (!asbd || !(asbd->mFormatFlags & kAudioFormatFlagIsFloat) || asbd->mBitsPerChannel != 32) return;
    CMItemCount frames = CMSampleBufferGetNumSamples(sb);
    if (frames <= 0) return;

    size_t need = 0;
    if (CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(sb, &need, NULL, 0, NULL, NULL, 0, NULL) != noErr || need == 0) return;
    AudioBufferList *abl = malloc(need);
    if (!abl) return;
    CMBlockBufferRef block = NULL;
    if (CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(sb, NULL, abl, need, NULL, NULL,
            kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment, &block) != noErr) {
        free(abl);
        return;
    }

    // Hand Go interleaved float32 stereo, whatever the layout.
    UInt32 ch = asbd->mChannelsPerFrame ? asbd->mChannelsPerFrame : 1;
    BOOL planar = (asbd->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
    float *out = malloc((size_t)frames * 2 * sizeof(float));
    if (out) {
        const float *b0 = abl->mBuffers[0].mData;
        const float *b1 = (planar && abl->mNumberBuffers > 1) ? abl->mBuffers[1].mData : NULL;
        UInt32 have0 = abl->mBuffers[0].mDataByteSize / sizeof(float);
        for (CMItemCount i = 0; i < frames; i++) {
            float l = 0, r = 0;
            if (planar) {
                if ((UInt32)i < have0) l = b0[i];
                r = b1 ? b1[i] : l;
            } else if ((UInt32)(i * ch + (ch > 1 ? 1 : 0)) < have0) {
                l = b0[i * ch];
                r = ch > 1 ? b0[i * ch + 1] : l;
            }
            out[2 * i] = l;
            out[2 * i + 1] = r;
        }
        hdAudioData(out, (int)(frames * 2 * sizeof(float)), self.token);
        free(out);
    }
    free(abl);
    if (block) CFRelease(block);
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    const char *msg = error ? [[error localizedDescription] UTF8String] : "stopped";
    hdAudioStopped(self.token, (char *)msg);
}

@end

static SCStream *hdStream = nil;
static HDAudioOutput *hdOut = nil;
static dispatch_queue_t hdQueue = NULL;

void hdAudioStop(void) {
    if (hdStream) {
        dispatch_semaphore_t done = dispatch_semaphore_create(0);
        [hdStream stopCaptureWithCompletionHandler:^(NSError *e) { dispatch_semaphore_signal(done); }];
        dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC));
        dispatch_release(done);
        [hdStream release];
        hdStream = nil;
    }
    if (hdOut) {
        [hdOut release];
        hdOut = nil;
    }
}

// Starts capturing the main display's sound. Returns NULL, or an error
// message the caller frees.
char *hdAudioStart(int token) {
    hdAudioStop();
    if (!hdQueue) hdQueue = dispatch_queue_create("hopdesk.audio", DISPATCH_QUEUE_SERIAL);

    __block char *err = NULL;
    __block SCDisplay *display = nil;
    dispatch_semaphore_t got = dispatch_semaphore_create(0);
    [SCShareableContent getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:YES
                                                 completionHandler:^(SCShareableContent *content, NSError *e) {
        if (e) err = strdup([[e localizedDescription] UTF8String]);
        else if (content.displays.count == 0) err = strdup("no display found");
        else display = [content.displays[0] retain];
        dispatch_semaphore_signal(got);
    }];
    long timedOut = dispatch_semaphore_wait(got, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC));
    dispatch_release(got);
    if (timedOut) return strdup("timed out asking macOS for the screen (is Screen Recording allowed?)");
    if (err) return err;

    SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];
    [display release];
    SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
    cfg.capturesAudio = YES;
    cfg.excludesCurrentProcessAudio = YES;
    cfg.sampleRate = 48000;
    cfg.channelCount = 2;
    // Only the sound is wanted: keep the (required) picture tiny and rare.
    cfg.width = 2;
    cfg.height = 2;
    cfg.minimumFrameInterval = CMTimeMake(1, 1);
    cfg.queueDepth = 3;

    hdOut = [[HDAudioOutput alloc] init];
    hdOut.token = token;
    hdStream = [[SCStream alloc] initWithFilter:filter configuration:cfg delegate:hdOut];
    [filter release];
    [cfg release];

    NSError *ae = nil;
    if (![hdStream addStreamOutput:hdOut type:SCStreamOutputTypeAudio sampleHandlerQueue:hdQueue error:&ae] ||
        ![hdStream addStreamOutput:hdOut type:SCStreamOutputTypeScreen sampleHandlerQueue:hdQueue error:&ae]) {
        char *m = strdup(ae ? [[ae localizedDescription] UTF8String] : "could not add the sound output");
        hdAudioStop();
        return m;
    }

    dispatch_semaphore_t started = dispatch_semaphore_create(0);
    [hdStream startCaptureWithCompletionHandler:^(NSError *se) {
        if (se) err = strdup([[se localizedDescription] UTF8String]);
        dispatch_semaphore_signal(started);
    }];
    timedOut = dispatch_semaphore_wait(started, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC));
    dispatch_release(started);
    if (timedOut && !err) err = strdup("timed out starting sound capture");
    if (err) {
        hdAudioStop();
        return err;
    }
    return NULL;
}
