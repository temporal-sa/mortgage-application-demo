import {
  Body,
  Controller,
  Get,
  HttpCode,
  HttpStatus,
  Param,
  Post,
  Query,
} from '@nestjs/common';
import {
  ApiBody,
  ApiOkResponse,
  ApiOperation,
  ApiParam,
  ApiQuery,
  ApiResponse,
  ApiTags,
} from '@nestjs/swagger';

import { MORTGAGE_EXAMPLE_APPLICATION_ID } from './constants';
import { ApplicationActionDto } from './dto/application-action.dto';
import { ApplicationListItemDto } from './dto/application-list-item.dto';
import { StartMortgageApplicationDto } from './dto/start-mortgage-application.dto';
import {
  MORTGAGE_SCENARIOS,
  MortgageScenarioOption,
} from './models/mortgage-scenario.type';
import { MortgageService } from './mortgage.service';

@ApiTags('Applications')
@Controller('applications')
export class MortgageController {
  constructor(private readonly mortgageService: MortgageService) {}

  @Post()
  @HttpCode(HttpStatus.ACCEPTED)
  @ApiOperation({ summary: 'Start a mortgage application workflow' })
  @ApiBody({ type: StartMortgageApplicationDto })
  @ApiResponse({ status: 202, description: 'Workflow started' })
  @ApiResponse({ status: 409, description: 'Workflow already exists' })
  startApplication(@Body() dto: StartMortgageApplicationDto) {
    return this.mortgageService.startApplication(
      dto.applicationId,
      dto.applicantName,
      dto.scenario,
      dto.externalFailureRatePercent,
    );
  }

  @Get()
  @ApiOperation({ summary: 'List all mortgage applications' })
  @ApiOkResponse({ type: ApplicationListItemDto, isArray: true })
  listApplications() {
    return this.mortgageService.listApplications();
  }

  @Get('scenarios')
  @ApiOperation({ summary: 'List available mortgage scenarios' })
  @ApiOkResponse({ description: 'Available scenarios' })
  getScenarios(): { scenarios: MortgageScenarioOption[] } {
    return { scenarios: MORTGAGE_SCENARIOS };
  }

  @Get(':applicationId')
  @ApiOperation({ summary: 'Get mortgage application state' })
  @ApiParam({
    name: 'applicationId',
    type: String,
    example: MORTGAGE_EXAMPLE_APPLICATION_ID,
  })
  @ApiQuery({
    name: 'runId',
    required: false,
    type: String,
    description:
      'Specific Temporal run to address. Use when the same applicationId has been reset or re-run, so the same workflowId now spans multiple executions.',
  })
  @ApiResponse({ status: 200, description: 'Current application state' })
  getApplication(
    @Param('applicationId') applicationId: string,
    @Query('runId') runId?: string,
  ) {
    return this.mortgageService.getApplication(applicationId, runId);
  }

  @Post(':applicationId/actions')
  @HttpCode(HttpStatus.ACCEPTED)
  @ApiOperation({ summary: 'Perform an operator action on an application' })
  @ApiParam({
    name: 'applicationId',
    type: String,
    example: MORTGAGE_EXAMPLE_APPLICATION_ID,
  })
  @ApiBody({ type: ApplicationActionDto })
  @ApiResponse({ status: 202, description: 'Action accepted' })
  @ApiResponse({ status: 400, description: 'Invalid action or payload' })
  @ApiResponse({ status: 404, description: 'Application not found' })
  performAction(
    @Param('applicationId') applicationId: string,
    @Body() dto: ApplicationActionDto,
  ) {
    return this.mortgageService.handleAction(applicationId, dto);
  }
}
