//go:build desktop && darwin

// 起動中の .app をもう一度開いたとき（kAEReopenApplication）に Go の looptrackReopen を呼ぶ（reopen_darwin.go）。
#import <Cocoa/Cocoa.h>

extern void looptrackReopen(void);

@interface LooptrackReopenHandler : NSObject
- (void)handleReopen:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply;
@end

@implementation LooptrackReopenHandler
- (void)handleReopen:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
  looptrackReopen();
}
@end

static LooptrackReopenHandler *looptrackReopenHandler;

// NSApplication は起動の終わりに reopen の既定の処理を登録するので、起動の後（トレイの onReady）にメインスレッドで差し替える。
void looptrackInstallReopen(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    looptrackReopenHandler = [[LooptrackReopenHandler alloc] init];
    [[NSAppleEventManager sharedAppleEventManager]
        setEventHandler:looptrackReopenHandler
            andSelector:@selector(handleReopen:withReplyEvent:)
          forEventClass:kCoreEventClass
             andEventID:kAEReopenApplication];
  });
}
