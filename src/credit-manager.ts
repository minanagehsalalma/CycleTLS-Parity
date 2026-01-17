/**
 * Credit Manager for CycleTLS V2 Flow Control
 *
 * Tracks bytes received from the server and sends credit replenishment
 * packets when a threshold is reached. This implements backpressure
 * from the client side.
 */

/**
 * CreditManager tracks received bytes and triggers credit replenishment.
 */
export class CreditManager {
  private bytesReceived: number = 0;
  private threshold: number;
  private sendCredits: (credits: number) => void;
  private paused: boolean = false;

  /**
   * Create a new CreditManager.
   *
   * @param threshold - Number of bytes to receive before sending credits
   * @param sendCredits - Callback to send credits to the server
   */
  constructor(threshold: number, sendCredits: (credits: number) => void) {
    this.threshold = threshold;
    this.sendCredits = sendCredits;
  }

  /**
   * Called when data is received from the server.
   * Will send credits when threshold is reached.
   *
   * @param bytes - Number of bytes received
   */
  onDataReceived(bytes: number): void {
    if (this.paused) {
      return;
    }

    this.bytesReceived += bytes;

    if (this.bytesReceived >= this.threshold) {
      this.sendCredits(this.bytesReceived);
      this.bytesReceived = 0;
    }
  }

  /**
   * Pause credit sending. Useful for implementing client-side backpressure.
   */
  pause(): void {
    this.paused = true;
  }

  /**
   * Resume credit sending and flush any accumulated bytes.
   */
  resume(): void {
    this.paused = false;
    if (this.bytesReceived >= this.threshold) {
      this.sendCredits(this.bytesReceived);
      this.bytesReceived = 0;
    }
  }

  /**
   * Flush any accumulated bytes as credits immediately.
   */
  flush(): void {
    if (this.bytesReceived > 0) {
      this.sendCredits(this.bytesReceived);
      this.bytesReceived = 0;
    }
  }

  /**
   * Reset the manager state.
   */
  reset(): void {
    this.bytesReceived = 0;
    this.paused = false;
  }
}
