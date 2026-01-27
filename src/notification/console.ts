import { NotificationService } from './interface';

/**
 * Console logger implementation of NotificationService
 * Logs all notifications to console with appropriate level
 */
export class ConsoleNotification implements NotificationService {
  async sendWarning(message: string): Promise<void> {
    console.warn(`[WARNING] ${new Date().toISOString()} - ${message}`);
  }

  async sendError(message: string): Promise<void> {
    console.error(`[ERROR] ${new Date().toISOString()} - ${message}`);
  }

  async sendInfo(message: string): Promise<void> {
    console.log(`[INFO] ${new Date().toISOString()} - ${message}`);
  }
}
