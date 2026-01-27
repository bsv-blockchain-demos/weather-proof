/**
 * Notification service interface for sending alerts and messages
 */
export interface NotificationService {
  /**
   * Send a warning notification
   */
  sendWarning(message: string): Promise<void>;

  /**
   * Send an error notification
   */
  sendError(message: string): Promise<void>;

  /**
   * Send an info notification
   */
  sendInfo(message: string): Promise<void>;
}
